// Compa Web Console - Web-based chat and management interface
//
// Provides a web UI for chatting with Compa via the web chat WebSocket,
// with configuration management and gateway process control.
//
// Usage:
//
//	go build -o compa ./web/backend/
//	./compa [config.json]
//	./compa -public config.json

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/netbind"
	"github.com/xibodev/compa/v3/web/backend/api"
	"github.com/xibodev/compa/v3/web/backend/launcherconfig"
	"github.com/xibodev/compa/v3/web/backend/middleware"
	"github.com/xibodev/compa/v3/web/backend/utils"
)

const (
	appName = "Compa"

	logPath   = "logs"
	panicFile = "launcher_panic.log"
	logFile   = "launcher.log"
)

var (
	appVersion = config.Version

	servers    []*http.Server
	serverAddr string
	// browserLaunch decides what openBrowser() opens, on auto-open and from
	// the tray's "Open" (see launcherBrowserLaunchSuffix).
	browserLaunch struct {
		setupToken string
		store      api.PasswordStore
		autoLogin  *middleware.LauncherDashboardLocalAutoLogin
	}
	apiHandler *api.Handler

	noBrowser *bool
)

const (
	// launcherAutoLoginTTL bounds how long a sign-in link the launcher opens
	// works; the browser uses it at once, and it works only once.
	launcherAutoLoginTTL = 2 * time.Minute
	// launcherReadHeaderTimeout, launcherIdleTimeout and
	// launcherMaxHeaderBytes bound what one connection may hold. Bodies are
	// bounded per request (middleware.APIBodyLimit).
	launcherReadHeaderTimeout = 10 * time.Second
	launcherIdleTimeout       = 2 * time.Minute
	launcherMaxHeaderBytes    = 64 << 10
)

// launcherUploadBodyLimits are the /api routes that take uploads larger than
// middleware.DefaultAPIBodyLimit; their handlers check the size themselves.
var launcherUploadBodyLimits = map[string]int64{
	// Recorded speech, as voice.go's maxVoiceUpload allows.
	"/api/voice/transcribe": 32 << 20,
}

func shouldEnableLauncherFileLogging(enableConsole, debug bool) bool {
	return !enableConsole || debug
}

func shouldEnableLocalAutoLogin(noBrowser bool, probeHost string) bool {
	return !noBrowser && isLoopbackLaunchHost(probeHost)
}

func isLoopbackLaunchHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	host = strings.Trim(host, "[]")
	if i := strings.LastIndex(host, "%"); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// launcherBrowserLaunchSuffix is the path the launcher opens: the setup page
// with its token while no password is set, else a fresh one-time sign-in
// link when this computer's browser may get one, else the dashboard.
func launcherBrowserLaunchSuffix(
	needsSetup bool,
	setupToken string,
	localAutoLogin *middleware.LauncherDashboardLocalAutoLogin,
) string {
	if needsSetup {
		return middleware.LauncherDashboardSetupURLPath(setupToken)
	}
	if localAutoLogin != nil {
		if path, err := localAutoLogin.Renew(launcherAutoLoginTTL); err == nil {
			return path
		}
	}
	return ""
}

// launcherNeedsSetup reports whether no dashboard password is set yet.
func launcherNeedsSetup(store api.PasswordStore) bool {
	if store == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	initialized, err := store.IsInitialized(ctx)
	return err == nil && !initialized
}

func resolveLauncherHostInput(flagHost string, explicitFlag bool, envHost string) (string, bool, error) {
	if explicitFlag {
		normalized, err := netbind.NormalizeHostInput(flagHost)
		if err != nil {
			return "", false, err
		}
		return normalized, true, nil
	}

	envHost = strings.TrimSpace(envHost)
	if envHost == "" {
		return "", false, nil
	}

	normalized, err := netbind.NormalizeHostInput(envHost)
	if err != nil {
		return "", false, err
	}
	return normalized, true, nil
}

func openLauncherListeners(hostInput string, public bool, port string) (netbind.OpenResult, error) {
	defaultMode := netbind.DefaultLoopback
	if strings.TrimSpace(hostInput) == "" && public {
		defaultMode = netbind.DefaultAny
	}

	plan, err := netbind.BuildPlan(hostInput, defaultMode)
	if err != nil {
		return netbind.OpenResult{}, err
	}
	return netbind.OpenPlan(plan, port)
}

func appendUniqueHost(hosts []string, seen map[string]struct{}, host string) []string {
	host = strings.TrimSpace(host)
	if host == "" {
		return hosts
	}
	key := strings.ToLower(host)
	if _, ok := seen[key]; ok {
		return hosts
	}
	seen[key] = struct{}{}
	return append(hosts, host)
}

func hasWildcardBindHosts(bindHosts []string) bool {
	for _, bindHost := range bindHosts {
		if netbind.IsUnspecifiedHost(bindHost) {
			return true
		}
	}
	return false
}

func wildcardBindHostFamilies(bindHosts []string) (hasIPv4, hasIPv6 bool) {
	for _, bindHost := range bindHosts {
		host := strings.TrimSpace(bindHost)
		if host == "" {
			continue
		}

		if !netbind.IsUnspecifiedHost(host) {
			continue
		}

		ip := net.ParseIP(strings.Trim(host, "[]"))
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			hasIPv4 = true
			continue
		}
		hasIPv6 = true
	}

	return hasIPv4, hasIPv6
}

func wildcardAdvertiseIP(bindHosts []string, ipv4, ipv6 string) string {
	hasIPv4Wildcard, hasIPv6Wildcard := wildcardBindHostFamilies(bindHosts)
	v4 := strings.TrimSpace(ipv4)
	v6 := strings.TrimSpace(ipv6)

	switch {
	case hasIPv4Wildcard && hasIPv6Wildcard:
		if v6 != "" {
			return v6
		}
		return v4
	case hasIPv6Wildcard:
		return v6
	case hasIPv4Wildcard:
		return v4
	default:
		return ""
	}
}

func advertiseIPForWildcardBindHosts(bindHosts []string) string {
	return wildcardAdvertiseIP(bindHosts, utils.GetLocalIPv4(), utils.GetLocalIPv6())
}

func appendLauncherConsoleHostList(hosts []string, seen map[string]struct{}, values []string) []string {
	for _, value := range values {
		hosts = appendUniqueHost(hosts, seen, value)
	}
	return hosts
}

func shouldShowLocalhostConsoleEntry(hostInput string) bool {
	normalizedHostInput := strings.TrimSpace(hostInput)
	if normalizedHostInput == "" {
		return true
	}

	for token := range strings.SplitSeq(normalizedHostInput, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if token == "*" || strings.EqualFold(token, "localhost") {
			return true
		}

		ip := net.ParseIP(strings.Trim(token, "[]"))
		if ip == nil {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			if ip4.String() == "127.0.0.1" || ip4.String() == "0.0.0.0" {
				return true
			}
			continue
		}
		if ip.String() == "::1" || ip.String() == "::" {
			return true
		}
	}

	return false
}

func isConsoleDisplayGlobalIPv6(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.To4() != nil {
		return false
	}
	ip = ip.To16()
	if ip == nil {
		return false
	}
	return ip[0]&0xe0 == 0x20
}

func launcherConsoleHostsWithLocalAddrs(
	hostInput string,
	public bool,
	ipv4s []string,
	globalIPv6s []string,
) []string {
	hosts := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)

	if shouldShowLocalhostConsoleEntry(hostInput) {
		hosts = appendUniqueHost(hosts, seen, "localhost")
	}

	normalizedHostInput := strings.TrimSpace(hostInput)
	if normalizedHostInput == "" {
		if public {
			hosts = appendLauncherConsoleHostList(hosts, seen, globalIPv6s)
			hosts = appendLauncherConsoleHostList(hosts, seen, ipv4s)
		}
		return hosts
	}

	hasStar := false
	hasIPv4Any := false
	hasIPv6Any := false
	for _, token := range strings.Split(normalizedHostInput, ",") {
		switch strings.TrimSpace(token) {
		case "*":
			hasStar = true
		case "0.0.0.0":
			hasIPv4Any = true
		case "::":
			hasIPv6Any = true
		}
	}

	if hasStar {
		hosts = appendLauncherConsoleHostList(hosts, seen, globalIPv6s)
		hosts = appendLauncherConsoleHostList(hosts, seen, ipv4s)
		return hosts
	}

	for _, token := range strings.Split(normalizedHostInput, ",") {
		token = strings.TrimSpace(token)
		if token == "" || strings.EqualFold(token, "localhost") || netbind.IsLoopbackHost(token) {
			continue
		}

		ip := net.ParseIP(strings.Trim(token, "[]"))
		switch {
		case token == "::":
			hosts = appendLauncherConsoleHostList(hosts, seen, globalIPv6s)
		case token == "0.0.0.0":
			hosts = appendLauncherConsoleHostList(hosts, seen, ipv4s)
		case ip != nil && ip.To4() != nil:
			if hasIPv4Any {
				continue
			}
			hosts = appendUniqueHost(hosts, seen, ip.String())
		case ip != nil:
			if hasIPv6Any {
				continue
			}
			if isConsoleDisplayGlobalIPv6(ip) {
				hosts = appendUniqueHost(hosts, seen, ip.String())
			}
		default:
			hosts = appendUniqueHost(hosts, seen, token)
		}
	}

	return hosts
}

func launcherConsoleHosts(hostInput string, public bool) []string {
	return launcherConsoleHostsWithLocalAddrs(
		hostInput,
		public,
		utils.GetLocalIPv4s(),
		utils.GetGlobalIPv6s(),
	)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

type launcherAllowlistBypassLogDecision struct {
	level   logger.LogLevel
	message string
	emit    bool
}

func launcherBindMayExposeBeyondLoopback(hostInput string, public bool) bool {
	normalizedHostInput := strings.TrimSpace(hostInput)
	if normalizedHostInput == "" {
		return public
	}

	for token := range strings.SplitSeq(normalizedHostInput, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if token == "*" || netbind.IsUnspecifiedHost(token) {
			return true
		}
		if strings.EqualFold(token, "localhost") || netbind.IsLoopbackHost(token) {
			continue
		}
		return true
	}

	return false
}

func launcherAllowlistBypassLogPolicy(
	hostInput string,
	public bool,
	cfg launcherconfig.Config,
) launcherAllowlistBypassLogDecision {
	if !launcherBindMayExposeBeyondLoopback(hostInput, public) || len(cfg.AllowedCIDRs) == 0 {
		return launcherAllowlistBypassLogDecision{}
	}

	switch cfg.AllowLocalhostBypassSource {
	case launcherconfig.BoolFieldPresent:
		if cfg.AllowLocalhostBypass {
			return launcherAllowlistBypassLogDecision{
				level:   logger.INFO,
				emit:    true,
				message: "Launcher public access uses allowed_cidrs with allow_localhost_bypass=true; same-host proxies or tunnels can bypass CIDR restrictions",
			}
		}
	case launcherconfig.BoolFieldNull:
		return launcherAllowlistBypassLogDecision{
			level:   logger.WARN,
			emit:    true,
			message: "Launcher public access uses allowed_cidrs with allow_localhost_bypass=null; default localhost bypass remains enabled, so same-host proxies or tunnels can bypass CIDR restrictions",
		}
	}

	return launcherAllowlistBypassLogDecision{}
}

func main() {
	port := flag.String("port", "18800", "Port to listen on")
	host := flag.String("host", "", "Host to listen on (overrides -public when set)")
	public := flag.Bool("public", false, "Listen on all interfaces (dual-stack) instead of localhost only")
	noBrowser = flag.Bool("no-browser", false, "Do not auto-open browser on startup")
	lang := flag.String("lang", "", "Language of the tray menu: en (English) or zh (Chinese). Default: from LANG, else English")
	console := flag.Bool("console", false, "Console mode, no GUI")
	setPassword := flag.String("password", "", "Set dashboard password (min 8 characters) and exit; - reads it from standard input")

	var debug bool
	flag.BoolVar(&debug, "d", false, "Enable debug logging")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")

	flag.Usage = func() {
		attachParentConsole()
		fmt.Fprintf(os.Stderr, "%s Launcher - Web console and gateway manager\n\n", appName)
		fmt.Fprintf(os.Stderr, "Usage: %s [options] [config.json]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Arguments:\n")
		fmt.Fprintf(os.Stderr, "  config.json    Path to the configuration file (default: ~/.compa/config.json)\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "      Use default config path in GUI mode\n")
		fmt.Fprintf(os.Stderr, "  %s ./config.json\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "      Specify a config file\n")
		fmt.Fprintf(
			os.Stderr,
			"  %s -public ./config.json\n",
			os.Args[0],
		)
		fmt.Fprintf(os.Stderr, "      Allow access from other devices on the local network\n")
		fmt.Fprintf(os.Stderr, "  %s -host :: ./config.json\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "      Bind launcher host explicitly with exact host semantics\n")
		fmt.Fprintf(os.Stderr, "  %s -console -d ./config.json\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "      Run in the terminal with debug logs enabled\n")
	}
	flag.Parse()
	// A Windows build has no console of its own; one started from a
	// terminal writes there.
	consoleAttached := false
	if *console || *setPassword != "" {
		consoleAttached = attachParentConsole()
	}

	// Resolve an explicit config before any subsystem asks for host state. When
	// COMPA_HOME is absent, a positional config owns the adjacent state
	// directory; otherwise auth/modules silently drift to the process default.
	configPath := utils.GetDefaultConfigPath()
	if flag.NArg() > 0 {
		configPath = flag.Arg(0)
	}
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		logger.Fatalf("Failed to resolve config path: %v", err)
	}
	if flag.NArg() > 0 {
		if err := bindLauncherHomeToExplicitConfig(absPath); err != nil {
			logger.Fatalf("Failed to bind launcher home to explicit config: %v", err)
		}
	}

	// Initialize logger
	homeDir := utils.GetHome()
	launcherPath := launcherconfig.PathForAppConfig(absPath)

	if *setPassword != "" {
		os.Exit(runSetPassword(*setPassword, homeDir, launcherPath))
	}

	f := filepath.Join(homeDir, logPath, panicFile)
	panicFunc, err := logger.InitPanic(f)
	if err != nil {
		panic(fmt.Sprintf("error initializing panic log: %v", err))
	}
	defer panicFunc()

	enableConsole := *console
	// The logger keeps the standard output it found at start, which an
	// attached console does not replace: its lines go to the log file then.
	fileLoggingEnabled := shouldEnableLauncherFileLogging(enableConsole, debug) || consoleAttached
	if fileLoggingEnabled {
		// GUI mode writes launcher logs to file. Debug mode keeps file logging enabled in console mode too.
		if !debug && !enableConsole {
			logger.DisableConsole()
		}

		f := filepath.Join(homeDir, logPath, logFile)
		if err = logger.EnableFileLogging(f); err != nil {
			panic(fmt.Sprintf("error enabling file logging: %v", err))
		}
		defer logger.DisableFileLogging()
	}
	if debug {
		logger.SetLevel(logger.DEBUG)
	}

	// Set language from command line or auto-detect
	if *lang != "" {
		SetLanguage(*lang)
	}

	err = utils.EnsureOnboarded(absPath)
	if err != nil {
		logger.Errorf("Warning: Failed to initialize %s config automatically: %v", appName, err)
	}
	if !debug {
		logger.SetLevelFromString(config.ResolveGatewayLogLevel(absPath))
	}
	applyLauncherLoggingSettings(absPath)

	logger.InfoC("web", fmt.Sprintf("%s launcher starting (version %s)...", appName, appVersion))
	logger.InfoC("web", fmt.Sprintf("%s Home: %s", appName, homeDir))
	if debug {
		logger.InfoC("web", "Debug mode enabled")
		logger.DebugC(
			"web",
			fmt.Sprintf(
				"Launcher flags: console=%t host=%q public=%t no_browser=%t config=%s",
				enableConsole,
				*host,
				*public,
				*noBrowser,
				absPath,
			),
		)
	}

	var explicitPort bool
	var explicitPublic bool
	var explicitHost bool
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "port":
			explicitPort = true
		case "host":
			explicitHost = true
		case "public":
			explicitPublic = true
		}
	})

	launcherCfg, err := launcherconfig.Load(launcherPath, launcherconfig.Default())
	if err != nil {
		logger.ErrorC("web", fmt.Sprintf("Warning: Failed to load %s: %v", launcherPath, err))
		launcherCfg = launcherconfig.Default()
	}

	effectivePort := *port
	effectivePublic := *public
	if !explicitPort {
		effectivePort = strconv.Itoa(launcherCfg.Port)
	}
	if !explicitPublic {
		effectivePublic = launcherCfg.Public
	}
	envHost := strings.TrimSpace(os.Getenv(launcherconfig.EnvLauncherHost))

	hostInput, hostOverrideActive, err := resolveLauncherHostInput(*host, explicitHost, envHost)
	if err != nil {
		logger.Fatalf("Invalid host %q: %v", firstNonEmpty(strings.TrimSpace(*host), envHost), err)
	}
	if hostOverrideActive {
		effectivePublic = false
	}

	if !explicitHost && hostOverrideActive {
		logger.InfoC("web", "Using launcher host from environment COMPA_LAUNCHER_HOST")
	}

	if hostOverrideActive && explicitPublic {
		logger.InfoC("web", "Ignoring -public because launcher host was explicitly set")
	}

	if decision := launcherAllowlistBypassLogPolicy(hostInput, effectivePublic, launcherCfg); decision.emit {
		switch decision.level {
		case logger.WARN:
			logger.WarnC("web", decision.message)
		default:
			logger.InfoC("web", decision.message)
		}
	}

	portNum, err := strconv.Atoi(effectivePort)
	if err != nil || portNum < 1 || portNum > 65535 {
		if err == nil {
			err = errors.New("must be in range 1-65535")
		}
		logger.Fatalf("Invalid port %q: %v", effectivePort, err)
	}

	// Open the bcrypt password store (creates the DB file on first run).
	passwordStore, closeStore, sqliteErr, authStoreErr := openPasswordStore(homeDir, launcherPath, launcherCfg)
	if closeStore != nil {
		defer closeStore()
	}
	switch {
	case authStoreErr != nil:
		logger.ErrorC("web", fmt.Sprintf("Warning: could not open auth store: %v", authStoreErr))
	case sqliteErr != nil:
		logger.InfoC(
			"web",
			fmt.Sprintf(
				"Dashboard SQLite password store unavailable on this platform; using launcher-config password storage: %v",
				sqliteErr,
			),
		)
	}

	needsInitialSetup := false
	if passwordStore != nil {
		initialized, initErr := passwordStore.IsInitialized(context.Background())
		if initErr != nil {
			logger.ErrorC("web", fmt.Sprintf("Warning: could not check dashboard password state: %v", initErr))
		} else {
			needsInitialSetup = !initialized
		}
	}

	// LAN access waits for a password unless the owner allowed otherwise:
	// the dashboard would face the network before anyone signed in once.
	if needsInitialSetup && launcherBindMayExposeBeyondLoopback(hostInput, effectivePublic) {
		if !launcherCfg.AllowLANWithoutPassword {
			logger.Fatalf(
				"LAN access needs a dashboard password first. Set one with `compa -password -`, or start Compa "+
					"without -public, -host or Enable LAN Access and set it in the browser; to start anyway, set "+
					"allow_lan_without_password to true in %s",
				launcherPath,
			)
		}
		logger.WarnC("web", "LAN access is on and no dashboard password is set yet (allow_lan_without_password)")
	}

	openResult, err := openLauncherListeners(hostInput, effectivePublic, effectivePort)
	if err != nil {
		logger.Fatalf("Failed to open launcher listener(s): %v", err)
	}
	listeners := openResult.Listeners

	dashboardSession, dashErr := middleware.NewLauncherDashboardSession()
	if dashErr != nil {
		logger.Fatalf("Dashboard auth setup failed: %v", dashErr)
	}
	// Only the person who started Compa sees this token, in the browser
	// the launcher opens or on its console; the first password needs it.
	setupToken, dashErr := middleware.NewLauncherDashboardSetupToken()
	if dashErr != nil {
		logger.Fatalf("Dashboard auth setup failed: %v", dashErr)
	}

	var localAutoLogin *middleware.LauncherDashboardLocalAutoLogin
	if shouldEnableLocalAutoLogin(*noBrowser, openResult.ProbeHost) {
		localAutoLogin, err = middleware.NewLauncherDashboardLocalAutoLogin(launcherAutoLoginTTL)
		if err != nil {
			logger.Fatalf("Failed to create local auto-login grant: %v", err)
		}
	}

	// Initialize Server components
	mux := http.NewServeMux()

	api.RegisterLauncherAuthRoutes(mux, api.LauncherAuthRouteOpts{
		Session:           dashboardSession,
		PasswordStore:     passwordStore,
		StoreError:        authStoreErr,
		SetupToken:        setupToken,
		TrustedProxyCIDRs: launcherCfg.TrustedProxyCIDRs,
	})

	apiHandler = api.NewHandler(absPath)
	apiHandler.SetDebug(debug)
	if _, err = apiHandler.EnsureWebChatChannel(); err != nil {
		logger.ErrorC("web", fmt.Sprintf("Warning: failed to ensure web channel on startup: %v", err))
	}
	apiHandler.SetServerOptions(portNum, effectivePublic, explicitPublic, launcherCfg.AllowedCIDRs)
	apiHandler.SetServerAccessOptions(
		launcherCfg.AllowLocalhostBypass,
		launcherCfg.TrustedProxyCIDRs,
	)
	apiHandler.SetServerBindHost(hostInput, hostOverrideActive)
	apiHandler.RegisterRoutes(mux)

	// Frontend Embedded Assets
	registerEmbedRoutes(mux)

	// A saved change the gateway can apply without a restart reaches it once
	// its request was answered (see api.Handler.ApplyLiveChanges).
	dashAuth := middleware.LauncherDashboardAuth(middleware.LauncherDashboardAuthConfig{
		Session:        dashboardSession,
		LocalAutoLogin: localAutoLogin,
	}, apiHandler.ApplyLiveChanges(mux))

	// The network policy runs before the auth check, so the login and setup
	// pages are as restricted as the rest.
	accessControlled, err := middleware.IPAllowlist(middleware.IPAllowlistConfig{
		AllowedCIDRs:         launcherCfg.AllowedCIDRs,
		AllowLocalhostBypass: launcherCfg.AllowLocalhostBypass,
		TrustedProxyCIDRs:    launcherCfg.TrustedProxyCIDRs,
	}, middleware.HostAllowlist(middleware.HostAllowlistConfig{
		ListenHosts:  []string{hostInput},
		AllowedHosts: launcherCfg.AllowedHosts,
	}, middleware.SameOriginGuard(
		middleware.APIBodyLimit(middleware.DefaultAPIBodyLimit, launcherUploadBodyLimits,
			middleware.JSONContentType(dashAuth),
		),
	)))
	if err != nil {
		logger.Fatalf("Invalid allowed CIDR configuration: %v", err)
	}

	// Apply middleware stack
	handler := middleware.Recoverer(
		middleware.Logger(
			middleware.SecurityHeaders(
				middleware.SecurityHeadersConfig{ScriptHashes: dashboardScriptHashes()},
				accessControlled,
			),
		),
	)

	// Share the local URL with the launcher runtime.
	serverAddr = fmt.Sprintf("http://%s", net.JoinHostPort(openResult.ProbeHost, effectivePort))
	browserLaunch.setupToken = setupToken
	browserLaunch.store = passwordStore
	browserLaunch.autoLogin = localAutoLogin
	setupURL := serverAddr + middleware.LauncherDashboardSetupURLPath(setupToken)

	// Print startup banner (console mode only).
	if enableConsole || debug {
		consoleHosts := launcherConsoleHosts(hostInput, effectivePublic)

		fmt.Print(utils.Banner)
		fmt.Println()
		if needsInitialSetup {
			fmt.Println("  First-time setup: create the dashboard password at")
			fmt.Println()
			fmt.Printf("    >> %s <<\n", setupURL)
			fmt.Println()
		}
		fmt.Println("  Dashboard address:")
		fmt.Println()
		for _, host := range consoleHosts {
			fmt.Printf("    >> http://%s <<\n", net.JoinHostPort(host, effectivePort))
		}
		fmt.Println()
	} else if needsInitialSetup {
		// The tray's output, such as the installer's launcher.out.
		fmt.Printf("First-time setup: create the dashboard password at %s\n", setupURL)
	}
	if needsInitialSetup {
		logger.InfoC("web", "No dashboard password is set yet; the setup link is in the browser Compa opens, its console output and the tray's Open Console")
	}

	// Log startup info to file
	for _, ln := range listeners {
		logger.InfoC("web", fmt.Sprintf("Server will listen on http://%s", ln.Addr().String()))
	}
	if hasWildcardBindHosts(openResult.BindHosts) {
		if ip := advertiseIPForWildcardBindHosts(openResult.BindHosts); ip != "" {
			logger.InfoC("web", fmt.Sprintf("Public access enabled at http://%s", net.JoinHostPort(ip, effectivePort)))
		}
	}

	// Auto-start gateway after backend starts listening.
	go func() {
		time.Sleep(1 * time.Second)
		apiHandler.TryAutoStartGateway()
	}()

	// Start the server(s) in goroutines.
	servers = make([]*http.Server, 0, len(listeners))
	for _, ln := range listeners {
		srv := &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: launcherReadHeaderTimeout,
			IdleTimeout:       launcherIdleTimeout,
			MaxHeaderBytes:    launcherMaxHeaderBytes,
		}
		servers = append(servers, srv)

		go func(s *http.Server, l net.Listener) {
			logger.InfoC("web", fmt.Sprintf("Server listening on %s", l.Addr().String()))
			if serveErr := s.Serve(l); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				logger.Fatalf("Server failed to start on %s: %v", l.Addr().String(), serveErr)
			}
		}(srv, ln)
	}

	defer shutdownApp()

	// Start system tray or run in console mode
	if enableConsole {
		if !*noBrowser {
			if err := openBrowser(); err != nil {
				logger.Errorf("Warning: Failed to auto-open browser: %v", err)
			}
		}

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		<-sigChan
		logger.Info("Shutting down...")
	} else {
		// GUI mode: start system tray
		runTray()
	}
}

func bindLauncherHomeToExplicitConfig(configPath string) error {
	if os.Getenv(config.EnvHome) != "" {
		return nil
	}
	return os.Setenv(config.EnvHome, filepath.Dir(configPath))
}

// applyLauncherLoggingSettings applies config.json's logging settings,
// redaction and rotation, to the launcher's log. A config that doesn't load
// keeps the logger's defaults: redaction on, 10 MB × 5 files. A config save
// applies them again (see api.ApplyLoggingSettings).
func applyLauncherLoggingSettings(configPath string) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return
	}
	api.ApplyLoggingSettings(cfg)
}

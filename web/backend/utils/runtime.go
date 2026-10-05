package utils

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/logger"
)

// GetHome returns the Compa home directory.
// Priority: $COMPA_HOME > ~/.compa
func GetHome() string {
	return config.GetHome()
}

// GetDefaultConfigPath returns the default path to the Compa config file.
func GetDefaultConfigPath() string {
	if configPath := os.Getenv(config.EnvConfig); configPath != "" {
		return configPath
	}
	return filepath.Join(GetHome(), "config.json")
}

// KernelName is the name of the harness executable the shell supervises. The
// kernel ships under this one name, beside the shell or on its own.
const KernelName = "compa-kernel"

// KernelBinaryName returns the kernel's executable file name on this platform.
func KernelBinaryName() string {
	if runtime.GOOS == "windows" {
		return KernelName + ".exe"
	}
	return KernelName
}

// FindKernelBinary locates the compa-kernel executable.
// Search order:
//  1. COMPA_BINARY environment variable (explicit override)
//  2. Same directory as the current executable
//  3. Falls back to "compa-kernel" and relies on $PATH
func FindKernelBinary() string {
	// BESIDE THE SHELL FIRST. A full-product install ships the kernel next to
	// the shell, so resolving there runs the user's chat against the harness
	// the shell was shipped with rather than an unrelated one that happens to
	// be on $PATH -- a different build, and nothing would say so.
	name := KernelBinaryName()

	if p := os.Getenv(config.EnvBinary); p != "" {
		if info, _ := os.Stat(p); info != nil && !info.IsDir() {
			return p
		}
	}

	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		// Say WHICH name was tried and where. The previous version logged a
		// Debugf naming only the launcher's own path, so a failed lookup was
		// indistinguishable from a spawn that failed for any other reason.
		logger.Warnf("no %s found in %s; "+
			"falling back to PATH lookup, which will fail if it is not installed",
			name, dir)
	}

	return name
}

func appendUniqueIP(addrs []string, seen map[string]struct{}, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return addrs
	}
	if _, ok := seen[value]; ok {
		return addrs
	}
	seen[value] = struct{}{}
	return append(addrs, value)
}

// GetLocalIPv4s returns all non-loopback local IPv4 addresses.
func GetLocalIPv4s() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	results := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP == nil || ipnet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil {
			results = appendUniqueIP(results, seen, ip4.String())
		}
	}
	return results
}

func isDisplayGlobalIPv6(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.To4() != nil {
		return false
	}
	ip = ip.To16()
	if ip == nil {
		return false
	}
	// Only show IPv6 global unicast addresses in 2000::/3.
	return ip[0]&0xe0 == 0x20
}

// GetGlobalIPv6s returns all IPv6 global unicast addresses.
func GetGlobalIPv6s() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	results := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP == nil {
			continue
		}
		ip := ipnet.IP
		if !isDisplayGlobalIPv6(ip) {
			continue
		}
		results = appendUniqueIP(results, seen, ip.String())
	}
	return results
}

// GetLocalIPv4 returns the first non-loopback local IPv4 address.
func GetLocalIPv4() string {
	addrs := GetLocalIPv4s()
	if len(addrs) == 0 {
		return ""
	}
	return addrs[0]
}

// GetLocalIPv6 returns the first IPv6 global unicast address.
func GetLocalIPv6() string {
	addrs := GetGlobalIPv6s()
	if len(addrs) == 0 {
		return ""
	}
	return addrs[0]
}

// OpenBrowser automatically opens the given URL in the default browser. The
// opener is waited for in the background, so it never lingers as a zombie.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = LauncherExecCommand("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		return fmt.Errorf("unsupported platform")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

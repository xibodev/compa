package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/xibodev/compa/web/backend/api"
	"github.com/xibodev/compa/web/backend/dashboardauth"
	"github.com/xibodev/compa/web/backend/launcherconfig"
)

// openPasswordStore opens the dashboard password store the server uses: the
// SQLite store, or launcher-config.json where SQLite is unavailable. The
// -password command opens the same one, so it sets the password the
// dashboard checks. closeStore may be nil.
func openPasswordStore(
	homeDir, launcherPath string,
	launcherCfg launcherconfig.Config,
) (store api.PasswordStore, closeStore func(), sqliteErr error, err error) {
	authStore, err := dashboardauth.New(homeDir)
	if err == nil {
		return authStore, func() { _ = authStore.Close() }, nil, nil
	}
	if errors.Is(err, dashboardauth.ErrUnsupportedPlatform) {
		return launcherconfig.NewPasswordStore(launcherPath, launcherCfg), nil, err, nil
	}
	return nil, nil, nil, err
}

// runSetPassword sets the dashboard password and returns the exit code. The
// value "-" reads the password from standard input instead, without echo at
// a terminal, so it stays out of process lists and shell history. The
// password is trimmed and checked like the web setup's.
func runSetPassword(value, homeDir, launcherPath string) int {
	if value == "-" {
		read, err := readPasswordInput(os.Stdin, os.Stderr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: could not read the password: %v\n", err)
			return 1
		}
		value = read
	}
	password := strings.TrimSpace(value)
	if err := api.ValidateDashboardPassword(password); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	launcherCfg, err := launcherconfig.Load(launcherPath, launcherconfig.Default())
	if err != nil {
		launcherCfg = launcherconfig.Default()
	}
	store, closeStore, _, err := openPasswordStore(homeDir, launcherPath, launcherCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not open the password store: %v\n", err)
		return 1
	}
	if closeStore != nil {
		defer closeStore()
	}
	if err := store.SetPassword(context.Background(), password); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to set password: %v\n", err)
		return 1
	}
	fmt.Println("✓ Dashboard password updated successfully.")
	return 0
}

// readPasswordInput reads one line: without echo when in is a terminal,
// after a prompt on prompt.
func readPasswordInput(in *os.File, prompt io.Writer) (string, error) {
	if term.IsTerminal(int(in.Fd())) {
		fmt.Fprint(prompt, "Dashboard password: ")
		raw, err := term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(prompt)
		return string(raw), err
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

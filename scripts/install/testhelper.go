//go:build ignore

// Command testhelper stands in for compa and compa-kernel in the installer
// tests (test-install.sh, test-install.ps1), and serves their fake release:
//
//	testhelper serve <folder> <port file>   serve <folder> on a free local port
//	testhelper                              behave like a running compa
//
// As compa it stays up for ten minutes, unless FAKE_COMPA=fail: then it adds
// a line to launcher.log in COMPA_HOME and exits at once, as compa does when
// its port is taken.
package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if len(os.Args) == 4 && os.Args[1] == "serve" {
		if err := serve(os.Args[2], os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, "testhelper:", err)
			os.Exit(1)
		}
		return
	}
	if os.Getenv("FAKE_COMPA") == "fail" {
		logs := filepath.Join(os.Getenv("COMPA_HOME"), "logs")
		if err := os.MkdirAll(logs, 0o755); err == nil {
			if f, err := os.OpenFile(filepath.Join(logs, "launcher.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				fmt.Fprintln(f, "Failed to open launcher listener(s): address already in use")
				f.Close()
			}
		}
		os.Exit(1)
	}
	time.Sleep(10 * time.Minute)
}

func serve(root, portFile string) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
	if err := os.WriteFile(portFile+".tmp", []byte(port), 0o644); err != nil {
		return err
	}
	if err := os.Rename(portFile+".tmp", portFile); err != nil {
		return err
	}
	return http.Serve(ln, http.FileServer(http.Dir(root)))
}

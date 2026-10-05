//go:build !android && ((!darwin && !freebsd) || cgo)

package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"fyne.io/systray"

	"github.com/xibodev/compa/pkg/logger"
	"github.com/xibodev/compa/web/backend/utils"
)

func runTray() {
	// SIGTERM or Ctrl+C quits like the tray's Quit: Run returns, and main's
	// deferred shutdown stops the gateway this launcher started.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		if _, ok := <-signals; ok {
			logger.Info("Shutting down...")
			systray.Quit()
		}
	}()
	systray.Run(onReady, onExit)
}

// onReady is called when the system tray is ready
func onReady() {
	// Set icon and tooltip
	systray.SetIcon(getIcon())
	systray.SetTooltip(fmt.Sprintf(T(AppTooltip), appName))

	// Create menu items
	mOpen := systray.AddMenuItem(T(MenuOpen), T(MenuOpenTooltip))
	mAbout := systray.AddMenuItem(T(MenuAbout), T(MenuAboutTooltip))

	// Add version info under About menu
	mVersion := mAbout.AddSubMenuItem(fmt.Sprintf(T(MenuVersion), appVersion), T(MenuVersionTooltip))
	mVersion.Disable()
	mRepo := mAbout.AddSubMenuItem(T(MenuGitHub), "")

	systray.AddSeparator()

	// Add restart option
	mRestart := systray.AddMenuItem(T(MenuRestart), T(MenuRestartTooltip))

	systray.AddSeparator()

	// Quit option
	mQuit := systray.AddMenuItem(T(MenuQuit), T(MenuQuitTooltip))

	// Handle menu clicks
	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				if err := openBrowser(); err != nil {
					logger.Errorf("Failed to open browser: %v", err)
				}

			case <-mVersion.ClickedCh:
				// Version info - do nothing, just shows current version

			case <-mRepo.ClickedCh:
				if err := utils.OpenBrowser("https://github.com/xibodev/compa"); err != nil {
					logger.Errorf("Failed to open GitHub: %v", err)
				}

			case <-mRestart.ClickedCh:
				fmt.Println("Restart request received...")
				if apiHandler != nil {
					if pid, err := apiHandler.RestartGateway(); err != nil {
						logger.Errorf("Failed to restart gateway: %v", err)
					} else {
						logger.Infof("Gateway restarted (PID: %d)", pid)
					}
				}

			case <-mQuit.ClickedCh:
				systray.Quit()
			}
		}
	}()

	if !*noBrowser {
		// Auto-open browser after systray is ready (if not disabled)
		// Check no-browser flag via environment or pass as parameter if needed
		if err := openBrowser(); err != nil {
			logger.Errorf("Warning: Failed to auto-open browser: %v", err)
		}
	}
}

// onExit is called when the system tray is exiting
func onExit() {
	logger.Info(T(Exiting))
}

package whatsapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite"

	"github.com/xibodev/compa/v4/pkg/config"
)

const (
	sqliteDriver   = "sqlite"
	whatsappDBName = "store.db"
)

// ErrNotLinked is what the channel logs when it starts with no WhatsApp
// account linked: it stays idle until one is.
var ErrNotLinked = errors.New("WhatsApp is not linked: link it with `compa-kernel auth whatsapp` or on the WhatsApp page")

// StorePath is where the linked account's session is kept: the channel's
// session_store_path, or whatsapp/ in the workspace.
func StorePath(cfg *config.Config, settings *config.WhatsAppSettings) string {
	if settings != nil && settings.SessionStorePath != "" {
		return settings.SessionStorePath
	}
	return filepath.Join(cfg.WorkspacePath(), "whatsapp")
}

// openStore opens the session store in dir and returns its device: an
// unlinked one when no account is linked.
func openStore(ctx context.Context, dir string, log waLog.Logger) (*sqlstore.Container, *store.Device, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create session store dir: %w", err)
	}
	db, err := sql.Open(sqliteDriver, "file:"+filepath.Join(dir, whatsappDBName)+"?_foreign_keys=on")
	if err != nil {
		return nil, nil, fmt.Errorf("open whatsapp store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err = db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	container := sqlstore.NewWithDB(db, sqliteDriver, log)
	if err = container.Upgrade(ctx); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("open whatsapp store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = container.Close()
		return nil, nil, fmt.Errorf("get device store: %w", err)
	}
	return container, device, nil
}

// Linked returns the phone number of the account linked in dir, or "" when
// none is.
func Linked(ctx context.Context, dir string) (string, error) {
	container, device, err := openStore(ctx, dir, waLog.Noop)
	if err != nil {
		return "", err
	}
	defer container.Close()
	if device.ID == nil {
		return "", nil
	}
	return device.ID.User, nil
}

// Link links a WhatsApp account as a linked device, keeping its session in
// dir. It calls onCode with each QR code to show; the codes change about
// every 20 seconds. It returns the linked phone number once the code is
// scanned, or an error when linking fails, times out or ctx ends.
func Link(ctx context.Context, dir string, onCode func(code string)) (string, error) {
	container, device, err := openStore(ctx, dir, waLog.Noop)
	if err != nil {
		return "", err
	}
	defer container.Close()
	if device.ID != nil {
		return device.ID.User, nil
	}

	client := whatsmeow.NewClient(device, waLog.Noop)
	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		return "", fmt.Errorf("get QR channel: %w", err)
	}
	if err := client.Connect(); err != nil {
		return "", fmt.Errorf("connect: %w", err)
	}
	defer client.Disconnect()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case evt, ok := <-qrChan:
			if !ok {
				return "", errors.New("linking ended without a scan")
			}
			switch evt.Event {
			case "code":
				onCode(evt.Code)
			case "success":
				if client.Store.ID == nil {
					return "", errors.New("linked, but no account was saved")
				}
				return client.Store.ID.User, nil
			case "timeout":
				return "", errors.New("the QR code was not scanned in time")
			default:
				return "", fmt.Errorf("linking failed: %s", evt.Event)
			}
		}
	}
}

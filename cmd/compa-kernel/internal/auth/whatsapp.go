package auth

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"

	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v3/pkg/channels/whatsapp"
	"github.com/xibodev/compa/v3/pkg/config"
)

func newWhatsAppCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "whatsapp",
		Short: "Link a WhatsApp account by QR code and turn on the WhatsApp channel",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			return linkWhatsApp(ctx, internal.GetConfigPath())
		},
	}
}

func linkWhatsApp(ctx context.Context, configPath string) error {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	var settings *config.WhatsAppSettings
	if bc := cfg.Channels.Get(config.ChannelWhatsApp); bc != nil {
		if decoded, err := bc.GetDecoded(); err == nil {
			settings, _ = decoded.(*config.WhatsAppSettings)
		}
	}

	fmt.Println("In WhatsApp on your phone: Settings > Linked devices > Link a device, then scan:")
	phone, err := whatsapp.Link(ctx, whatsapp.StorePath(cfg, settings), func(code string) {
		qrterminal.GenerateWithConfig(code, qrterminal.Config{Level: qrterminal.L, Writer: os.Stdout, HalfBlocks: true})
	})
	if err != nil {
		return fmt.Errorf("link WhatsApp: %w", err)
	}

	bc := cfg.Channels.Get(config.ChannelWhatsApp)
	if bc == nil {
		bc = &config.Channel{Type: config.ChannelWhatsApp}
		cfg.Channels[config.ChannelWhatsApp] = bc
	}
	bc.Enabled = true
	if err := config.SaveConfig(configPath, cfg); err != nil {
		return fmt.Errorf("linked +%s, but could not turn the channel on: %w", phone, err)
	}
	fmt.Printf("Linked +%s. The WhatsApp channel is on; restart the gateway to connect it.\n", phone)
	return nil
}

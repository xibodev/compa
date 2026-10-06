package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mymmrac/telego"

	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/logger"
	"github.com/xibodev/compa/v2/pkg/media"
	"github.com/xibodev/compa/v2/pkg/utils"
)

const fileDownloadTimeout = 60 * time.Second

// botTokenRe matches a Telegram bot token as it appears in API and file URLs.
var botTokenRe = regexp.MustCompile(`\d{5,}:[A-Za-z0-9_-]{30,}`)

// redactToken removes the bot token from text that may quote a Telegram URL,
// such as the error of a failed request, so that it can be logged.
func (c *TelegramChannel) redactToken(text string) string {
	if c == nil {
		return redactBotToken(text, nil)
	}
	return redactBotToken(text, c.bot)
}

func redactBotToken(text string, bot *telego.Bot) string {
	if bot != nil {
		if token := bot.Token(); token != "" {
			text = strings.ReplaceAll(text, token, "[FILTERED]")
		}
	}
	return botTokenRe.ReplaceAllString(text, "[FILTERED]")
}

// redactedError hides the bot token in an error's text and keeps the error
// chain for errors.Is and errors.As.
type redactedError struct {
	err  error
	text string
}

func (e *redactedError) Error() string { return e.text }
func (e *redactedError) Unwrap() error { return e.err }

// safeErr returns err with the bot token removed from its text. Errors leave
// the channel through it, because callers log them.
func (c *TelegramChannel) safeErr(err error) error {
	if c == nil {
		return safeBotErr(err, nil)
	}
	return safeBotErr(err, c.bot)
}

func safeBotErr(err error, bot *telego.Bot) error {
	if err == nil {
		return nil
	}
	text := redactBotToken(err.Error(), bot)
	if text == err.Error() {
		return err
	}
	return &redactedError{err: err, text: text}
}

// maxDownloadBytes is the largest inbound file the channel downloads.
func (c *TelegramChannel) maxDownloadBytes() int64 {
	if c.maxMediaBytes > 0 {
		return c.maxMediaBytes
	}
	return config.DefaultMaxMediaSize
}

func (c *TelegramChannel) downloadHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if c.tgCfg != nil && c.tgCfg.Proxy != "" {
		if proxyURL, err := url.Parse(c.tgCfg.Proxy); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}
	return &http.Client{Timeout: fileDownloadTimeout, Transport: transport}
}

// downloadFileWithInfo downloads a file Telegram described into the media
// directory, refusing files over the media size limit. The download URL holds
// the bot token, so the URL, and errors quoting it, are never logged; the file
// is named by its file_id instead.
func (c *TelegramChannel) downloadFileWithInfo(file *telego.File, ext string) string {
	if file == nil || file.FilePath == "" {
		return ""
	}
	maxBytes := c.maxDownloadBytes()
	if file.FileSize > maxBytes {
		logger.WarnCF("telegram", "File is over the media size limit, not downloading it", map[string]any{
			"file_id":   file.FileID,
			"size":      file.FileSize,
			"max_bytes": maxBytes,
		})
		return ""
	}

	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, fileDownloadTimeout)
	defer cancel()

	localPath, err := c.fetchFile(ctx, c.bot.FileDownloadURL(file.FilePath), file.FilePath+ext, maxBytes)
	if err != nil {
		logger.ErrorCF("telegram", "Failed to download file", map[string]any{
			"file_id": file.FileID,
			"error":   c.redactToken(err.Error()),
		})
		return ""
	}
	logger.DebugCF("telegram", "File downloaded", map[string]any{"file_id": file.FileID, "path": localPath})
	return localPath
}

func (c *TelegramChannel) fetchFile(ctx context.Context, fileURL, filename string, maxBytes int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.downloadHTTPClient().Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return "", urlErr.Err
		}
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	mediaDir := media.TempDir()
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		return "", err
	}
	localPath := filepath.Join(mediaDir, uuid.New().String()[:8]+"_"+utils.SanitizeFilename(filename))
	out, err := os.Create(localPath)
	if err != nil {
		return "", err
	}
	written, err := io.Copy(out, io.LimitReader(resp.Body, maxBytes+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil && written > maxBytes {
		err = fmt.Errorf("file is over the media size limit of %d bytes", maxBytes)
	}
	if err != nil {
		_ = os.Remove(localPath)
		return "", err
	}
	return localPath, nil
}

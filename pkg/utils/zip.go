package utils

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xibodev/compa/pkg/logger"
)

// Limits of an archive ExtractZipFile accepts: a file of at most
// maxZipEntrySize, at most maxZipEntries entries, and maxZipTotalSize in all,
// counted on what is actually extracted rather than what headers declare.
const (
	maxZipEntrySize  = 5 << 20
	errZipLimitsText = "zip archive exceeds the extraction limits"
)

// Variables so tests can lower them.
var (
	maxZipEntries         = 10000
	maxZipTotalSize int64 = 200 << 20
)

// ExtractZipFile extracts a ZIP archive from disk to targetDir.
// It reads entries one at a time from disk, keeping memory usage minimal.
//
// Security: rejects path traversal attempts and symlinks, and archives past
// the size and entry limits above.
func ExtractZipFile(zipPath string, targetDir string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("invalid ZIP: %w", err)
	}
	defer reader.Close()

	logger.DebugCF("zip", "Extracting ZIP", map[string]any{
		"zip_path":   zipPath,
		"target_dir": targetDir,
		"entries":    len(reader.File),
	})

	if len(reader.File) > maxZipEntries {
		return fmt.Errorf("%s: %d entries (max %d)", errZipLimitsText, len(reader.File), maxZipEntries)
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("failed to create target dir: %w", err)
	}

	var total int64
	for _, f := range reader.File {
		// Path traversal protection.
		cleanName := filepath.Clean(f.Name)
		if strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
			return fmt.Errorf("zip entry has unsafe path: %q", f.Name)
		}

		destPath := filepath.Join(targetDir, cleanName)

		// Double-check the resolved path is within target directory (defense-in-depth).
		targetDirClean := filepath.Clean(targetDir)
		if !strings.HasPrefix(filepath.Clean(destPath), targetDirClean+string(filepath.Separator)) &&
			filepath.Clean(destPath) != targetDirClean {
			return fmt.Errorf("zip entry escapes target dir: %q", f.Name)
		}

		mode := f.FileInfo().Mode()

		// Reject any symlink.
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("zip contains symlink %q; symlinks are not allowed", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(destPath, 0o755); err != nil {
				return err
			}
			continue
		}

		// Ensure parent directory exists.
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return err
		}

		written, err := extractSingleFile(f, destPath, maxZipTotalSize-total)
		if err != nil {
			return err
		}
		total += written
	}

	return nil
}

// extractSingleFile extracts one zip.File entry to destPath, with a size
// check: at most maxZipEntrySize, and at most remaining, what the archive's
// total may still grow by. It returns the size extracted.
func extractSingleFile(f *zip.File, destPath string, remaining int64) (int64, error) {
	limit := min(int64(maxZipEntrySize), remaining)

	// Check the uncompressed size from the header, if available.
	if f.UncompressedSize64 > maxZipEntrySize {
		return 0, fmt.Errorf("zip entry %q is too large (%d bytes)", f.Name, f.UncompressedSize64)
	}
	if f.UncompressedSize64 > uint64(max(limit, 0)) {
		return 0, fmt.Errorf("%s: more than %d bytes in all", errZipLimitsText, maxZipTotalSize)
	}

	rc, err := f.Open()
	if err != nil {
		return 0, fmt.Errorf("failed to open zip entry %q: %w", f.Name, err)
	}
	defer rc.Close()

	outFile, err := os.Create(destPath)
	if err != nil {
		return 0, fmt.Errorf("failed to create file %q: %w", destPath, err)
	}

	// Streamed size check: prevent overruns and malicious/corrupt headers.
	written, err := io.CopyN(outFile, rc, limit+1)
	// Closed before any removal: Windows cannot remove an open file.
	closeErr := outFile.Close()
	switch {
	case err != nil && err != io.EOF:
		_ = os.Remove(destPath)
		return 0, fmt.Errorf("failed to extract %q: %w", f.Name, err)
	case written > limit:
		_ = os.Remove(destPath)
		if limit < maxZipEntrySize {
			return 0, fmt.Errorf("%s: more than %d bytes in all", errZipLimitsText, maxZipTotalSize)
		}
		return 0, fmt.Errorf("zip entry %q exceeds max size (%d bytes)", f.Name, written)
	case closeErr != nil:
		_ = os.Remove(destPath)
		logger.ErrorCF("zip", "Failed to close file", map[string]any{
			"dest_path": destPath,
			"error":     closeErr.Error(),
		})
		return 0, fmt.Errorf("failed to write %q: %w", f.Name, closeErr)
	}
	return written, nil
}

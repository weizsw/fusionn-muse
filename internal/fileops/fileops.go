package fileops

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/fusionn-muse/pkg/logger"
)

// HardlinkOrCopy tries to hardlink src to dst, falls back to copy if hardlink fails.
func HardlinkOrCopy(src, dst string) error {
	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	// Try hardlink first
	err := os.Link(src, dst)
	if err == nil {
		logger.Debugf("🔗 Hard-linked: %s → %s", src, dst)
		return nil
	}

	logger.Debugf("⚠️ Hardlink failed (%v), falling back to copy", err)

	// Fallback to copy
	if err := copyFile(src, dst); err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	logger.Debugf("📋 Copied: %s → %s", src, dst)
	return nil
}

// Move moves a file from src to dst.
func Move(src, dst string) error {
	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	// Try rename first (works if same filesystem)
	err := os.Rename(src, dst)
	if err == nil {
		logger.Debugf("📦 Moved: %s → %s", src, dst)
		return nil
	}

	// Fallback: copy then delete
	if err := copyFile(src, dst); err != nil {
		return fmt.Errorf("copy for move: %w", err)
	}

	if err := os.Remove(src); err != nil {
		logger.Warnf("⚠️ Failed to remove source after copy: %v", err)
	}

	logger.Debugf("📦 Moved (copy+delete): %s → %s", src, dst)
	return nil
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

// EnsureDir creates a directory if it doesn't exist.
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0755)
}

// Exists checks if a file or directory exists.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// IsVideoFile checks if the file has a video extension.
func IsVideoFile(path string) bool {
	ext := filepath.Ext(path)
	videoExts := map[string]bool{
		".mkv":  true,
		".mp4":  true,
		".avi":  true,
		".mov":  true,
		".wmv":  true,
		".flv":  true,
		".webm": true,
		".m4v":  true,
		".ts":   true,
	}
	return videoExts[ext]
}

// FindVideoFiles returns all video files in a directory (recursive).
func FindVideoFiles(dir string) ([]string, error) {
	var videos []string

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && IsVideoFile(path) {
			videos = append(videos, path)
		}
		return nil
	})

	return videos, err
}

// ChangeExtension changes the extension of a filename.
func ChangeExtension(path, newExt string) string {
	ext := filepath.Ext(path)
	return path[:len(path)-len(ext)] + newExt
}

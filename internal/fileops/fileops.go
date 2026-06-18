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
	if samePath(src, dst) {
		return nil
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if err := removeExistingDestination(dst); err != nil {
		return err
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
	if samePath(src, dst) {
		return nil
	}
	if same, err := sameFile(src, dst); err != nil {
		return err
	} else if same {
		if err := os.Remove(src); err != nil {
			return fmt.Errorf("remove source after same-inode move: %w", err)
		}
		logger.Debugf("📦 Moved: %s → %s", src, dst)
		return nil
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

// Copy copies src to dst, replacing dst if it exists.
func Copy(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	if samePath(src, dst) {
		return nil
	}
	if err := removeExistingDestination(dst); err != nil {
		return err
	}
	if err := copyFile(src, dst); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	logger.Debugf("📋 Copied: %s → %s", src, dst)
	return nil
}

func removeExistingDestination(dst string) error {
	if _, err := os.Stat(dst); err == nil {
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("remove existing destination: %w", err)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat destination: %w", err)
	}
	return nil
}

func sameFile(a, b string) (bool, error) {
	aInfo, err := os.Stat(a)
	if err != nil {
		return false, fmt.Errorf("stat source: %w", err)
	}
	bInfo, err := os.Stat(b)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat destination: %w", err)
	}
	return os.SameFile(aInfo, bInfo), nil
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
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

// Remove deletes a file.
func Remove(path string) error {
	return os.Remove(path)
}

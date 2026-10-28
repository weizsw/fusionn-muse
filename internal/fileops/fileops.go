package fileops

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.uber.org/zap"

	"github.com/fusionn-muse/pkg/logger"
)

// HardlinkOrCopy tries to hardlink src to dst, falls back to copy if hardlink fails.
func HardlinkOrCopy(ctx context.Context, src, dst string) error {
	log := logger.Component("fileops")
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
		log.Debug(ctx, "file_linked", "File hard-linked", zap.String("src", src), zap.String("dst", dst))
		return nil
	}

	log.Debug(ctx, "link_fallback", "Hardlink failed; copying instead", zap.Error(err))

	// Fallback to copy
	if err := copyFile(ctx, src, dst); err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	log.Debug(ctx, "file_copied", "File copied", zap.String("src", src), zap.String("dst", dst))
	return nil
}

// HardlinkOrCopyNoReplace creates dst without overwriting an existing file.
func HardlinkOrCopyNoReplace(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	log := logger.Component("fileops")
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	if samePath(src, dst) {
		return nil
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if err := os.Link(src, dst); err == nil {
		file, err := os.Open(dst)
		if err != nil {
			return err
		}
		err = file.Sync()
		_ = file.Close()
		if err != nil {
			return err
		}
		log.Debug(ctx, "file_linked", "File hard-linked", zap.String("src", src), zap.String("dst", dst))
		return SyncDir(filepath.Dir(dst))
	} else if os.IsExist(err) {
		return fmt.Errorf("destination exists: %w", err)
	}
	if err := copyFileNoReplace(ctx, src, dst); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	log.Debug(ctx, "file_copied", "File copied", zap.String("src", src), zap.String("dst", dst))
	return SyncDir(filepath.Dir(dst))
}

// Move moves a file from src to dst.
func Move(ctx context.Context, src, dst string) error {
	log := logger.Component("fileops")
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
		log.Debug(ctx, "file_moved", "File moved", zap.String("src", src), zap.String("dst", dst))
		return nil
	}

	// Try rename first (works if same filesystem)
	err := os.Rename(src, dst)
	if err == nil {
		log.Debug(ctx, "file_moved", "File moved", zap.String("src", src), zap.String("dst", dst))
		return nil
	}

	// Fallback: copy then delete
	if err := copyFile(ctx, src, dst); err != nil {
		return fmt.Errorf("copy for move: %w", err)
	}

	if err := os.Remove(src); err != nil {
		log.Warn(ctx, "source_remove_failed", "Source could not be removed after copy", zap.Error(err))
	}

	log.Debug(ctx, "file_moved", "File moved by copy", zap.String("src", src), zap.String("dst", dst))
	return nil
}

// MoveNoReplace publishes the entire file before removing src, without replacing dst.
func MoveNoReplace(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if samePath(src, dst) {
		_, err := os.Stat(src)
		return err
	}
	if err := HardlinkOrCopyNoReplace(ctx, src, dst); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("remove source after move: %w", err)
	}
	return SyncDir(filepath.Dir(src))
}

// Copy copies src to dst, replacing dst if it exists.
func Copy(ctx context.Context, src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	if samePath(src, dst) {
		return nil
	}
	if err := removeExistingDestination(dst); err != nil {
		return err
	}
	if err := copyFile(ctx, src, dst); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	logger.Component("fileops").Debug(ctx, "file_copied", "File copied", zap.String("src", src), zap.String("dst", dst))
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
func copyFile(ctx context.Context, src, dst string) error {
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

	if _, err := io.Copy(dstFile, contextReader{ctx, srcFile}); err != nil {
		return err
	}
	if err := dstFile.Sync(); err != nil {
		return err
	}
	return dstFile.Close()
}

func copyFileNoReplace(ctx context.Context, src, dst string) (err error) {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}
	dstFile, err := os.CreateTemp(filepath.Dir(dst), ".muse-copy-*")
	if err != nil {
		return err
	}
	defer os.Remove(dstFile.Name())
	defer dstFile.Close()
	if err := dstFile.Chmod(srcInfo.Mode()); err != nil {
		return err
	}
	if _, err := io.Copy(dstFile, contextReader{ctx, srcFile}); err != nil {
		return err
	}
	if err := dstFile.Sync(); err != nil {
		return err
	}
	if err := dstFile.Close(); err != nil {
		return err
	}
	return os.Link(dstFile.Name(), dst)
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

// SyncDir makes a completed rename or link durable before a Stage checkpoint.
func SyncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

type contextReader struct {
	context.Context
	io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(buffer)
}

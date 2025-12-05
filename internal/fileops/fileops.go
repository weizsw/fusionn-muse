package fileops

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fusionn-muse/pkg/logger"
)

// MinVideoSize is the minimum file size (200MB) for a valid video file.
// Files smaller than this are likely ads, samples, or bonus content.
const MinVideoSize int64 = 200 * 1024 * 1024

// codePattern matches video codes like SONE-269, JUR-123
// Format: [2-5 letters]-[3-5 digits]
// Removes suffixes like -C, -1, etc.
var codePattern = regexp.MustCompile(`([A-Z]{2,5}-\d{3,5})`)

// subtitleSuffixPattern matches -C or -c suffix before extension (indicates subtitle already exists)
var subtitleSuffixPattern = regexp.MustCompile(`(?i)-c\.[^.]+$`)

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

// Remove deletes a file.
func Remove(path string) error {
	return os.Remove(path)
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

// HasVideoCode checks if a filename contains a valid video code pattern.
// Matches codes like SONE-269, JUR-123 anywhere in the filename (handles prefixes).
func HasVideoCode(filename string) bool {
	return codePattern.MatchString(strings.ToUpper(filename))
}

// FindValidVideoFile finds the single valid video file in a directory.
// Filters by: code pattern match AND size > MinVideoSize.
// Returns the largest file if multiple match, or error if none found.
func FindValidVideoFile(dir string) (string, error) {
	videos, err := FindVideoFiles(dir)
	if err != nil {
		return "", err
	}

	var bestPath string
	var bestSize int64

	for _, path := range videos {
		filename := filepath.Base(path)

		// Check code pattern
		if !HasVideoCode(filename) {
			logger.Debugf("⏭️  Skipped (no code pattern): %s", filename)
			continue
		}

		// Check file size
		info, err := os.Stat(path)
		if err != nil {
			logger.Debugf("⏭️  Skipped (stat error): %s: %v", filename, err)
			continue
		}

		size := info.Size()
		if size <= MinVideoSize {
			logger.Debugf("⏭️  Skipped (too small: %dMB): %s", size/(1024*1024), filename)
			continue
		}

		// Track largest valid file
		if size > bestSize {
			bestPath = path
			bestSize = size
		}
	}

	if bestPath == "" {
		return "", fmt.Errorf("no valid video file found (need code pattern + size > %dMB)", MinVideoSize/(1024*1024))
	}

	if len(videos) > 1 {
		logger.Debugf("✅ Selected largest valid video: %s (%dMB)", filepath.Base(bestPath), bestSize/(1024*1024))
	}

	return bestPath, nil
}

// ChangeExtension changes the extension of a filename.
func ChangeExtension(path, newExt string) string {
	ext := filepath.Ext(path)
	return path[:len(path)-len(ext)] + newExt
}

// HasSubtitleSuffix checks if filename has -C suffix (indicates subtitle already exists).
// Examples: SONE-269-C.mp4 → true, SONE-269.mp4 → false
func HasSubtitleSuffix(filename string) bool {
	return subtitleSuffixPattern.MatchString(filename)
}

// CleanVideoFilename extracts the video code from messy filenames.
// Examples:
//   - SONE-269.mp4 → SONE-269.mp4 (unchanged)
//   - sone-269.mp4 → SONE-269.mp4 (uppercase)
//   - SONE-269-C.mp4 → SONE-269.mp4 (removes -C suffix)
//   - xxxSONE-269.mp4 → SONE-269.mp4 (removes prefix)
//
// Returns original filename if no code pattern found.
func CleanVideoFilename(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	match := codePattern.FindString(strings.ToUpper(filename))
	if match == "" {
		return filename // No pattern found, return as-is
	}
	return match + ext
}

// WriteDummySubtitle creates a dummy SRT file for dry-run testing.
func WriteDummySubtitle(path string) error {
	content := `1
00:00:00,000 --> 00:00:05,000
[Dry run test subtitle]

2
00:00:05,000 --> 00:00:10,000
This is a dummy subtitle for testing the workflow.
`
	return os.WriteFile(path, []byte(content), 0644)
}

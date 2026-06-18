package mediaintake

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fusionn-muse/internal/toolrun"
)

type CommandRunner = toolrun.Runner
type ExecCommandRunner = toolrun.ExecRunner

func prepareMultipart(req ResolveRequest, parts []string) (*ResolvedMedia, error) {
	code, ok := mediaCodeFor(parts[0], req.Path, req.TorrentName)
	if !ok {
		return nil, noValidMediaf("no code found for multipart video")
	}

	out := filepath.Join(req.StagingDir, code+".mkv")
	prepared, err := concatVideos(req.Context, req.Runner, parts, out)
	if err != nil {
		return nil, err
	}

	resolved := &ResolvedMedia{
		SourcePath:  prepared,
		FileName:    filepath.Base(prepared),
		StagingPath: prepared,
		Code:        code,
	}
	if anyChineseSubtitle(parts) {
		resolved.HasChineseSubtitle = true
		resolved.SubtitleDetectionReason = SubtitleDetectionFilename
	} else {
		detectExistingSubtitle(req.Context, resolved, prepared, req.Path)
	}
	return resolved, nil
}

func prepareImage(req ResolveRequest, imagePath string) (*ResolvedMedia, error) {
	code, ok := imageCodeFor(req, imagePath)
	if !ok {
		return nil, noValidMediaf("no code found in image filename, folder, or torrent name")
	}

	extractDir := imageExtractionDir(req.StagingDir, code)
	if imageExtractionOverlapsSource(extractDir, imagePath) || imageExtractionOverlapsSource(extractDir, req.Path) {
		return nil, fmt.Errorf("image extraction dir overlaps source path: %s", extractDir)
	}
	if err := os.RemoveAll(extractDir); err != nil {
		return nil, fmt.Errorf("clear image extraction dir: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(extractDir)
	}()
	if err := extractImage(req.Context, req.Runner, imagePath, extractDir); err != nil {
		return nil, fmt.Errorf("image extraction failed: %w", err)
	}

	out := filepath.Join(req.StagingDir, code+".mkv")
	parts, err := selectExtractedMedia(req.Context, extractDir)
	if err != nil {
		return nil, err
	}

	var prepared string
	if len(parts) == 1 {
		prepared, err = remuxVideo(req.Context, req.Runner, parts[0], out)
	} else {
		prepared, err = concatVideos(req.Context, req.Runner, parts, out)
	}
	if err != nil {
		return nil, err
	}

	resolved := &ResolvedMedia{
		SourcePath:  prepared,
		FileName:    filepath.Base(prepared),
		StagingPath: prepared,
		Code:        code,
	}
	if HasChineseSubtitle(filepath.Base(imagePath)) || anyChineseSubtitle(parts) {
		resolved.HasChineseSubtitle = true
		resolved.SubtitleDetectionReason = SubtitleDetectionFilename
	} else {
		detectExistingSubtitle(req.Context, resolved, prepared, imageFallbackFolder(req.Path))
	}
	return resolved, nil
}

func imageCodeFor(req ResolveRequest, imagePath string) (string, bool) {
	if filepath.Clean(req.Path) == filepath.Clean(imagePath) {
		return bestCodeFor(imagePath, req.TorrentName)
	}
	return mediaCodeFor(imagePath, imageFallbackFolder(req.Path), req.TorrentName)
}

func imageFallbackFolder(path string) string {
	info, err := os.Stat(path)
	if err == nil && !info.IsDir() {
		return filepath.Dir(path)
	}
	return path
}

func imageExtractionDir(stagingDir, code string) string {
	return filepath.Join(filepath.Dir(stagingDir), "media-extract", code+"-image")
}

func imageExtractionOverlapsSource(extractDir, sourcePath string) bool {
	extractAbs, err := filepath.Abs(extractDir)
	if err != nil {
		extractAbs = filepath.Clean(extractDir)
	}
	sourceAbs, err := filepath.Abs(sourcePath)
	if err != nil {
		sourceAbs = filepath.Clean(sourcePath)
	}
	return pathContains(extractAbs, sourceAbs) || pathContains(sourceAbs, extractAbs)
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func concatVideos(ctx context.Context, runner CommandRunner, parts []string, out string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}

	listPath := filepath.Join(filepath.Dir(out), strings.TrimSuffix(filepath.Base(out), filepath.Ext(out))+".concat.txt")
	if err := writeConcatList(listPath, parts); err != nil {
		return "", err
	}
	defer func() {
		_ = os.Remove(listPath)
	}()

	if err := runner.Run(ctx, "ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", listPath, "-c", "copy", out); err != nil {
		if removeErr := os.Remove(out); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return "", fmt.Errorf("remove partial concat output: %w", removeErr)
		}
		if transcodeErr := runner.Run(ctx, "ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", listPath, "-map", "0:v:0", "-map", "0:a:0?", "-c:v", "libx264", "-c:a", "aac", out); transcodeErr != nil {
			_ = os.Remove(out)
			return "", fmt.Errorf("concat copy failed: %w; transcode failed: %w", err, transcodeErr)
		}
		return out, nil
	}

	return out, nil
}

func remuxVideo(ctx context.Context, runner CommandRunner, in, out string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}

	if err := runner.Run(ctx, "ffmpeg", "-y", "-i", in, "-map", "0:v:0", "-map", "0:a?", "-c", "copy", out); err != nil {
		if removeErr := os.Remove(out); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return "", fmt.Errorf("remove partial remux output: %w", removeErr)
		}
		if transcodeErr := runner.Run(ctx, "ffmpeg", "-y", "-i", in, "-map", "0:v:0", "-map", "0:a:0?", "-c:v", "libx264", "-c:a", "aac", out); transcodeErr != nil {
			_ = os.Remove(out)
			return "", fmt.Errorf("remux copy failed: %w; transcode failed: %w", err, transcodeErr)
		}
		return out, nil
	}

	return out, nil
}

func writeConcatList(path string, parts []string) error {
	var b strings.Builder
	for _, part := range parts {
		escaped := strings.ReplaceAll(part, "'", "'\\''")
		b.WriteString("file '")
		b.WriteString(escaped)
		b.WriteString("'\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}

func extractImage(ctx context.Context, runner CommandRunner, imagePath, outDir string) error {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("create image extraction dir: %w", err)
	}

	switch strings.ToLower(filepath.Ext(imagePath)) {
	case ".iso":
		if err := runner.Run(ctx, "bsdtar", "-xf", imagePath, "-C", outDir); err == nil {
			return nil
		}
		return runner.Run(ctx, "7z", "x", "-y", "-o"+outDir, imagePath)
	case ".nrg":
		if err := runner.Run(ctx, "7z", "x", "-y", "-o"+outDir, imagePath); err == nil {
			return nil
		}
		isoPath := filepath.Join(outDir, strings.TrimSuffix(filepath.Base(imagePath), filepath.Ext(imagePath))+".iso")
		if err := runner.Run(ctx, "nrg2iso", imagePath, isoPath); err != nil {
			return err
		}
		return runner.Run(ctx, "bsdtar", "-xf", isoPath, "-C", outDir)
	case ".img", ".mdf", ".bin":
		return runner.Run(ctx, "7z", "x", "-y", "-o"+outDir, imagePath)
	default:
		return fmt.Errorf("unsupported image extension: %s", filepath.Ext(imagePath))
	}
}

func anyChineseSubtitle(paths []string) bool {
	for _, path := range paths {
		if HasChineseSubtitle(filepath.Base(path)) {
			return true
		}
	}
	return false
}

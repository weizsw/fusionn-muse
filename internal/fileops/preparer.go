package fileops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) error
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func prepareMultipart(req ResolveRequest, parts []string) (*ResolvedMedia, error) {
	code, ok := ExtractVideoCode(filepath.Base(parts[0]))
	if !ok {
		code, ok = fallbackCode(req.Path, req.TorrentName)
	}
	if !ok {
		return nil, fmt.Errorf("no code found for multipart video")
	}

	out := filepath.Join(req.StagingDir, code+".mkv")
	prepared, err := concatVideos(req.Context, req.Runner, parts, out)
	if err != nil {
		return nil, err
	}

	return &ResolvedMedia{
		SourcePath:         prepared,
		FileName:           filepath.Base(prepared),
		StagingPath:        prepared,
		Code:               code,
		HasChineseSubtitle: anyChineseSubtitle(parts),
	}, nil
}

func prepareImage(req ResolveRequest, imagePath string) (*ResolvedMedia, error) {
	code, ok := imageCodeFor(imagePath, imageFallbackFolder(req.Path), req.TorrentName)
	if !ok {
		return nil, fmt.Errorf("no code found in image filename, folder, or torrent name")
	}

	extractDir := filepath.Join(req.StagingDir, code+"-image")
	if err := os.RemoveAll(extractDir); err != nil {
		return nil, fmt.Errorf("clear image extraction dir: %w", err)
	}
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

	return &ResolvedMedia{
		SourcePath:         prepared,
		FileName:           filepath.Base(prepared),
		StagingPath:        prepared,
		Code:               code,
		HasChineseSubtitle: HasChineseSubtitle(filepath.Base(imagePath)) || anyChineseSubtitle(parts),
	}, nil
}

func imageFallbackFolder(path string) string {
	info, err := os.Stat(path)
	if err == nil && !info.IsDir() {
		return filepath.Dir(path)
	}
	return path
}

func concatVideos(ctx context.Context, runner CommandRunner, parts []string, out string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}

	listPath := filepath.Join(filepath.Dir(out), strings.TrimSuffix(filepath.Base(out), filepath.Ext(out))+".concat.txt")
	if err := writeConcatList(listPath, parts); err != nil {
		return "", err
	}

	if err := runner.Run(ctx, "ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", listPath, "-c", "copy", out); err != nil {
		if removeErr := os.Remove(out); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return "", fmt.Errorf("remove partial concat output: %w", removeErr)
		}
		mp4Out := ChangeExtension(out, ".mp4")
		if transcodeErr := runner.Run(ctx, "ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", listPath, "-c:v", "libx264", "-c:a", "aac", mp4Out); transcodeErr != nil {
			return "", fmt.Errorf("concat copy failed: %w; transcode failed: %w", err, transcodeErr)
		}
		return mp4Out, nil
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
		mp4Out := ChangeExtension(out, ".mp4")
		if transcodeErr := runner.Run(ctx, "ffmpeg", "-y", "-i", in, "-map", "0:v:0", "-map", "0:a?", "-c:v", "libx264", "-c:a", "aac", mp4Out); transcodeErr != nil {
			return "", fmt.Errorf("remux copy failed: %w; transcode failed: %w", err, transcodeErr)
		}
		return mp4Out, nil
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

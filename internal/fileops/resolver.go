package fileops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ResolveRequest struct {
	Context     context.Context
	Path        string
	TorrentName string
	StagingDir  string
}

type ResolvedMedia struct {
	SourcePath         string
	FileName           string
	StagingPath        string
	Code               string
	HasChineseSubtitle bool
}

type mediaCandidate struct {
	Path string
	Name string
	Size int64
	Code string
}

var imageExts = map[string]bool{
	".iso": true,
	".nrg": true,
	".img": true,
	".mdf": true,
	".bin": true,
}

// IsImageFile checks for disc/archive image sources that may contain playable media.
func IsImageFile(path string) bool {
	return imageExts[strings.ToLower(filepath.Ext(path))]
}

func ResolveMedia(req ResolveRequest) (*ResolvedMedia, error) {
	if req.Context == nil {
		req.Context = context.Background()
	}
	if req.StagingDir == "" {
		return nil, fmt.Errorf("staging dir required")
	}

	info, err := os.Stat(req.Path)
	if err != nil {
		return nil, fmt.Errorf("path does not exist: %w", err)
	}

	if !info.IsDir() {
		if IsVideoFile(req.Path) {
			return resolveSingleVideo(req.Path, req.TorrentName, false)
		}
		if IsImageFile(req.Path) {
			return nil, fmt.Errorf("image preparation not implemented")
		}
		return nil, fmt.Errorf("unsupported media path: %s", req.Path)
	}

	return resolveFolder(req)
}

func resolveSingleVideo(path, torrentName string, requireCode bool) (*ResolvedMedia, error) {
	code, ok := bestCodeFor(path, torrentName)
	if requireCode && !ok {
		return nil, fmt.Errorf("no code found in filename, folder, or torrent name")
	}

	fileName := filepath.Base(path)
	if ok {
		fileName = code + strings.ToLower(filepath.Ext(path))
	}

	return &ResolvedMedia{
		SourcePath:         path,
		FileName:           fileName,
		Code:               code,
		HasChineseSubtitle: HasChineseSubtitle(filepath.Base(path)),
	}, nil
}

func resolveFolder(req ResolveRequest) (*ResolvedMedia, error) {
	videos, _, err := findMediaCandidates(req.Path)
	if err != nil {
		return nil, err
	}

	if best := bestVideoCandidate(videos, req.Path, req.TorrentName); best != nil {
		return resolveSingleVideo(best.Path, req.TorrentName, true)
	}

	return nil, fmt.Errorf("no valid video file found (need code pattern + size > %dMB)", MinVideoSize/(1024*1024))
}

func findMediaCandidates(dir string) ([]mediaCandidate, []mediaCandidate, error) {
	var videos []mediaCandidate
	var images []mediaCandidate

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if IsVideoFile(path) {
			if info.Size() <= MinVideoSize {
				return nil
			}
			code, _ := ExtractVideoCode(filepath.Base(path))
			videos = append(videos, mediaCandidate{Path: path, Name: filepath.Base(path), Size: info.Size(), Code: code})
			return nil
		}
		if IsImageFile(path) {
			images = append(images, mediaCandidate{Path: path, Name: filepath.Base(path), Size: info.Size()})
		}
		return nil
	})

	return videos, images, err
}

func bestVideoCandidate(videos []mediaCandidate, folder, torrentName string) *mediaCandidate {
	if len(videos) == 0 {
		return nil
	}

	var coded []mediaCandidate
	for _, video := range videos {
		if video.Code != "" {
			coded = append(coded, video)
		}
	}
	if len(coded) > 0 {
		sort.Slice(coded, func(i, j int) bool { return coded[i].Size > coded[j].Size })
		return &coded[0]
	}

	if _, ok := fallbackCode(folder, torrentName); !ok {
		return nil
	}

	sort.Slice(videos, func(i, j int) bool { return videos[i].Size > videos[j].Size })
	return &videos[0]
}

func bestCodeFor(path, torrentName string) (string, bool) {
	if code, ok := ExtractVideoCode(filepath.Base(path)); ok {
		return code, true
	}
	return fallbackCode(filepath.Dir(path), torrentName)
}

func fallbackCode(folder, torrentName string) (string, bool) {
	if code, ok := ExtractVideoCode(filepath.Base(folder)); ok {
		return code, true
	}
	if code, ok := ExtractVideoCode(torrentName); ok {
		return code, true
	}
	return "", false
}

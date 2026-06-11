package fileops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type ResolveRequest struct {
	Context     context.Context
	Path        string
	TorrentName string
	StagingDir  string
	Runner      CommandRunner
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

type partCandidate struct {
	path  string
	order int
}

var imageExts = map[string]bool{
	".iso": true,
	".nrg": true,
	".img": true,
	".mdf": true,
	".bin": true,
}

var (
	partWordPattern    = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(part|cd|disc)0*([1-9]\d*)(?:[^a-z0-9]|$)`)
	trailingNumberPart = regexp.MustCompile(`(?i)([a-z]+)0*\d{3,5}[a-z]*([1-9]\d*)$`)
	trailingLetterPart = regexp.MustCompile(`(?i)([a-z]+)-?0*\d{3,5}([a-z])(?:[^a-z0-9].*)?$`)
)

// IsImageFile checks for disc/archive image sources that may contain playable media.
func IsImageFile(path string) bool {
	return imageExts[strings.ToLower(filepath.Ext(path))]
}

func ResolveMedia(req ResolveRequest) (*ResolvedMedia, error) {
	if req.Context == nil {
		req.Context = context.Background()
	}
	if req.Runner == nil {
		req.Runner = ExecCommandRunner{}
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

	return resolveSelectedVideo(path, code), nil
}

func resolveSelectedVideo(path, code string) *ResolvedMedia {
	fileName := filepath.Base(path)
	if code != "" {
		fileName = code + strings.ToLower(filepath.Ext(path))
	}

	return &ResolvedMedia{
		SourcePath:         path,
		FileName:           fileName,
		Code:               code,
		HasChineseSubtitle: HasChineseSubtitle(filepath.Base(path)),
	}
}

func resolveFolder(req ResolveRequest) (*ResolvedMedia, error) {
	videos, _, err := findMediaCandidates(req.Context, req.Path)
	if err != nil {
		return nil, err
	}

	parts := findMultipartSet(videos, req.Path, req.TorrentName)
	if len(parts) > 1 {
		return prepareMultipart(req, parts)
	}
	if hasIncompleteMultipartSet(videos, req.Path, req.TorrentName) {
		return nil, fmt.Errorf("incomplete multipart video set")
	}

	if best := bestVideoCandidate(videos, req.Path, req.TorrentName); best != nil {
		return resolveSelectedVideo(best.Path, best.Code), nil
	}

	return nil, fmt.Errorf("no valid video file found (need code pattern + size > %dMB)", MinVideoSize/(1024*1024))
}

func findMediaCandidates(ctx context.Context, dir string) ([]mediaCandidate, []mediaCandidate, error) {
	var videos []mediaCandidate
	var images []mediaCandidate

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
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

	code, ok := fallbackCode(folder, torrentName)
	if !ok {
		return nil
	}

	sort.Slice(videos, func(i, j int) bool { return videos[i].Size > videos[j].Size })
	videos[0].Code = code
	return &videos[0]
}

func findMultipartSet(videos []mediaCandidate, folder, torrentName string) []string {
	if len(videos) < 2 {
		return nil
	}

	groups := multipartGroups(videos, folder, torrentName)
	codes := make([]string, 0, len(groups))
	for code := range groups {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	for _, code := range codes {
		parts := groups[code]
		if len(parts) < 2 {
			continue
		}
		if !validPartOrders(parts) {
			continue
		}
		sort.Slice(parts, func(i, j int) bool { return parts[i].order < parts[j].order })
		result := make([]string, 0, len(parts))
		for _, part := range parts {
			result = append(result, part.path)
		}
		return result
	}

	return nil
}

func hasIncompleteMultipartSet(videos []mediaCandidate, folder, torrentName string) bool {
	for _, parts := range multipartGroups(videos, folder, torrentName) {
		if len(parts) > 1 && !validPartOrders(parts) {
			return true
		}
	}
	return false
}

func multipartGroups(videos []mediaCandidate, folder, torrentName string) map[string][]partCandidate {
	groups := make(map[string][]partCandidate)
	for _, video := range videos {
		code := video.Code
		if code == "" {
			if fallback, ok := fallbackCode(folder, torrentName); ok {
				code = fallback
			}
		}
		if code == "" {
			continue
		}
		order, ok := detectPartOrder(video.Name)
		if !ok {
			continue
		}
		groups[code] = append(groups[code], partCandidate{path: video.Path, order: order})
	}

	return groups
}

func detectPartOrder(name string) (int, bool) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if match := partWordPattern.FindStringSubmatch(base); match != nil {
		n, err := strconv.Atoi(match[2])
		return n, err == nil
	}
	if match := trailingNumberPart.FindStringSubmatch(base); match != nil {
		n, err := strconv.Atoi(match[2])
		return n, err == nil
	}
	if match := trailingLetterPart.FindStringSubmatch(base); match != nil {
		letter := strings.ToUpper(match[2])
		if len(letter) == 1 && letter[0] >= 'A' && letter[0] <= 'Z' {
			return int(letter[0]-'A') + 1, true
		}
	}
	return 0, false
}

func validPartOrders(parts []partCandidate) bool {
	seen := make(map[int]bool, len(parts))
	maxOrder := 0
	for _, part := range parts {
		if seen[part.order] {
			return false
		}
		seen[part.order] = true
		if part.order > maxOrder {
			maxOrder = part.order
		}
	}
	if maxOrder != len(parts) {
		return false
	}
	for order := 1; order <= maxOrder; order++ {
		if !seen[order] {
			return false
		}
	}
	return true
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

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

type multipartGroupKey struct {
	codeRank  int
	code      string
	extFamily string
	partBase  string
}

type codeMatch struct {
	code string
	rank int
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
	trailingNumberPart = regexp.MustCompile(`(?i)([a-z]+)0*\d{3,5}[a-z]+([1-9]\d*)(?:[^a-z0-9].*)?$`)
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
			return prepareImage(req, req.Path)
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
	videos, images, err := findMediaCandidates(req.Context, req.Path)
	if err != nil {
		return nil, err
	}

	if best := bestFilenameCodedVideoCandidate(videos); best != nil {
		return resolveSelectedVideo(best.Path, best.Code), nil
	}

	parts := findMultipartSet(videos, req.Path, req.TorrentName)
	if len(parts) > 1 {
		return prepareMultipart(req, parts)
	}

	if best := bestVideoCandidate(videos, req.Path, req.TorrentName); best != nil {
		return resolveSelectedVideo(best.Path, best.Code), nil
	}
	if hasIncompleteMultipartSet(videos, req.Path, req.TorrentName) {
		return nil, fmt.Errorf("incomplete multipart video set")
	}

	if image := bestImageCandidate(images, req.Path, req.TorrentName); image != nil {
		return prepareImage(req, image.Path)
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

func bestFilenameCodedVideoCandidate(videos []mediaCandidate) *mediaCandidate {
	var coded []mediaCandidate
	for _, video := range videos {
		if video.Code == "" {
			continue
		}
		if _, _, ok := detectPartInfo(video.Name); ok {
			continue
		}
		coded = append(coded, video)
	}
	if len(coded) == 0 {
		return nil
	}

	sort.Slice(coded, func(i, j int) bool { return coded[i].Size > coded[j].Size })
	return &coded[0]
}

func bestVideoCandidate(videos []mediaCandidate, folder, torrentName string) *mediaCandidate {
	if len(videos) == 0 {
		return nil
	}

	var coded []mediaCandidate
	bestRank := 0
	for _, video := range videos {
		if _, _, ok := detectPartInfo(video.Name); ok {
			continue
		}
		if match, ok := mediaCodeMatchFor(video.Path, folder, torrentName); ok {
			video.Code = match.code
			if len(coded) == 0 || match.rank < bestRank {
				bestRank = match.rank
				coded = coded[:0]
			}
			if match.rank != bestRank {
				continue
			}
			coded = append(coded, video)
		}
	}
	if len(coded) > 0 {
		sort.Slice(coded, func(i, j int) bool { return coded[i].Size > coded[j].Size })
		return &coded[0]
	}
	return nil
}

func bestImageCandidate(images []mediaCandidate, folder, torrentName string) *mediaCandidate {
	if len(images) == 0 {
		return nil
	}

	var coded []mediaCandidate
	bestRank := 0
	for _, image := range images {
		if match, ok := mediaCodeMatchFor(image.Path, folder, torrentName); ok {
			if len(coded) == 0 || match.rank < bestRank {
				bestRank = match.rank
				coded = coded[:0]
			}
			if match.rank != bestRank {
				continue
			}
			coded = append(coded, image)
		}
	}
	if len(coded) > 0 {
		sort.Slice(coded, func(i, j int) bool { return coded[i].Size > coded[j].Size })
		return &coded[0]
	}

	sort.Slice(images, func(i, j int) bool { return images[i].Size > images[j].Size })
	return &images[0]
}

func findMultipartSet(videos []mediaCandidate, folder, torrentName string) []string {
	if len(videos) < 2 {
		return nil
	}

	groups := multipartGroups(videos, folder, torrentName)
	keys := make([]multipartGroupKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].codeRank != keys[j].codeRank {
			return keys[i].codeRank < keys[j].codeRank
		}
		if keys[i].code != keys[j].code {
			return keys[i].code < keys[j].code
		}
		if keys[i].extFamily != keys[j].extFamily {
			return keys[i].extFamily < keys[j].extFamily
		}
		return keys[i].partBase < keys[j].partBase
	})

	for _, key := range keys {
		parts := groups[key]
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
		if len(parts) > 0 && !validPartOrders(parts) {
			return true
		}
	}
	return false
}

func multipartGroups(videos []mediaCandidate, folder, torrentName string) map[multipartGroupKey][]partCandidate {
	groups := make(map[multipartGroupKey][]partCandidate)
	for _, video := range videos {
		match, ok := mediaCodeMatchFor(video.Path, folder, torrentName)
		if !ok {
			continue
		}
		order, partBase, ok := detectPartInfo(video.Name)
		if !ok {
			continue
		}
		key := multipartGroupKey{codeRank: match.rank, code: match.code, extFamily: videoExtensionFamily(video.Path), partBase: partBase}
		groups[key] = append(groups[key], partCandidate{path: video.Path, order: order})
	}

	return groups
}

func videoExtensionFamily(path string) string {
	return strings.ToLower(filepath.Ext(path))
}

func mediaCodeFor(path, folder, torrentName string) (string, bool) {
	match, ok := mediaCodeMatchFor(path, folder, torrentName)
	return match.code, ok
}

func mediaCodeMatchFor(path, folder, torrentName string) (codeMatch, bool) {
	if code, ok := ExtractVideoCode(filepath.Base(path)); ok {
		return codeMatch{code: code, rank: 0}, true
	}
	for _, dir := range candidateCodeFolders(path, folder) {
		if code, ok := ExtractVideoCode(filepath.Base(dir)); ok {
			return codeMatch{code: code, rank: 1}, true
		}
	}
	if code, ok := ExtractVideoCode(torrentName); ok {
		return codeMatch{code: code, rank: 2}, true
	}
	return codeMatch{}, false
}

func candidateCodeFolders(path, requestPath string) []string {
	candidateDir := filepath.Clean(filepath.Dir(path))
	requestDir := filepath.Clean(imageFallbackFolder(requestPath))
	var dirs []string
	for dir := candidateDir; ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if dir == requestDir {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return dirs
}

func detectPartInfo(name string) (int, string, bool) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if match := partWordPattern.FindStringSubmatchIndex(base); match != nil {
		n, err := strconv.Atoi(base[match[4]:match[5]])
		if err != nil {
			return 0, "", false
		}
		return n, normalizePartBase(base[:match[0]] + base[match[1]:]), true
	}
	if match := trailingNumberPart.FindStringSubmatchIndex(base); match != nil {
		n, err := strconv.Atoi(base[match[4]:match[5]])
		if err != nil {
			return 0, "", false
		}
		return n, normalizePartBase(base[:match[4]] + base[match[5]:]), true
	}
	if match := trailingLetterPart.FindStringSubmatchIndex(base); match != nil {
		letter := strings.ToUpper(base[match[4]:match[5]])
		if len(letter) == 1 && letter[0] >= 'A' && letter[0] <= 'Z' {
			return int(letter[0]-'A') + 1, normalizePartBase(base[:match[4]] + base[match[5]:]), true
		}
	}
	return 0, "", false
}

func normalizePartBase(base string) string {
	base = strings.Trim(strings.ToLower(base), " ._-")
	fields := strings.FieldsFunc(base, func(r rune) bool {
		return r == ' ' || r == '.' || r == '_' || r == '-'
	})
	return strings.Join(fields, "-")
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

func selectExtractedMedia(ctx context.Context, dir string) ([]string, error) {
	if parts := selectDVDTitleChain(dir); len(parts) > 0 {
		return parts, nil
	}
	if stream := selectLargestByGlob(
		filepath.Join(dir, "BDMV", "STREAM", "*.m2ts"),
		filepath.Join(dir, "BDMV", "STREAM", "*.M2TS"),
	); stream != "" {
		return []string{stream}, nil
	}

	videos, _, err := findMediaCandidates(ctx, dir)
	if err != nil {
		return nil, err
	}
	if parts := findMultipartSet(videos, dir, ""); len(parts) > 1 {
		return parts, nil
	}
	if hasIncompleteMultipartSet(videos, dir, "") {
		return nil, fmt.Errorf("incomplete multipart video set in extracted image")
	}
	if best := bestVideoCandidate(videos, dir, ""); best != nil {
		return []string{best.Path}, nil
	}

	return nil, fmt.Errorf("no media found in extracted image")
}

func selectDVDTitleChain(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "VIDEO_TS", "VTS_*_*.VOB"))
	if len(matches) == 0 {
		return nil
	}

	groups := make(map[string][]string)
	for _, path := range matches {
		name := strings.ToUpper(filepath.Base(path))
		if strings.HasSuffix(name, "_0.VOB") {
			continue
		}
		if len(name) < len("VTS_01_1.VOB") {
			continue
		}
		groups[name[:6]] = append(groups[name[:6]], path)
	}

	var best []string
	bestKey := ""
	var bestSize int64
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := groups[key]
		sort.Strings(group)
		var size int64
		for _, path := range group {
			info, err := os.Stat(path)
			if err == nil {
				size += info.Size()
			}
		}
		if size > bestSize || (size == bestSize && (bestKey == "" || key < bestKey)) {
			best = group
			bestKey = key
			bestSize = size
		}
	}

	if bestSize <= MinVideoSize {
		return nil
	}
	return best
}

func selectLargestByGlob(patterns ...string) string {
	var best string
	var bestSize int64
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		for _, path := range matches {
			info, err := os.Stat(path)
			if err == nil && info.Size() > MinVideoSize && info.Size() > bestSize {
				best = path
				bestSize = info.Size()
			}
		}
	}
	return best
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

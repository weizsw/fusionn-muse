# Media Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build media resolution so torrent folders with compact filenames, ordered multipart videos, and playable disc/archive images resolve into one normalized media job.

**Architecture:** Add focused helpers in `internal/fileops` for code parsing, source discovery, multipart ordering, and preparation command execution. Integrate the resolver in `handler.TorrentComplete` before job creation. Prepared multipart/image outputs are written to staging and recorded on `job.StagingPath` so the existing processor skips the staging copy and continues normally.

**Tech Stack:** Go 1.23, standard library filesystem/regexp/os/exec APIs, existing Gin handler and queue, `ffmpeg`, `bsdtar`, `7z`, and `nrg2iso` in Docker.

---

## File Structure

- Create `internal/fileops/code.go`: normalized video code parsing shared by filtering, cleaning, and resolver fallback.
- Create `internal/fileops/code_test.go`: code parser tests for hyphenated and compact names.
- Create `internal/fileops/resolver.go`: media source discovery, selection, multipart grouping, and high-level resolution.
- Create `internal/fileops/resolver_test.go`: temporary-directory tests for fallback selection and multipart ordering.
- Create `internal/fileops/preparer.go`: command runner, ffmpeg concat/remux/transcode, and image extractor registry helpers.
- Create `internal/fileops/preparer_test.go`: tests for command construction with a fake runner.
- Modify `internal/fileops/fileops.go`: reuse code parser in `HasVideoCode` and `CleanVideoFilename`; add image/video extensions only where needed.
- Modify `internal/handler/handler.go`: call resolver instead of `FindValidVideoFile`, preserve light-job detection, set `job.StagingPath` for prepared outputs.
- Modify `Dockerfile`: add image extraction tools.
- Modify `README.md`: document compact fallback, multipart assembly, and disc/archive image support.

---

### Task 1: Add Normalized Code Parser

**Files:**
- Create: `internal/fileops/code.go`
- Create: `internal/fileops/code_test.go`
- Modify: `internal/fileops/fileops.go`

- [ ] **Step 1: Write failing parser tests**

Create `internal/fileops/code_test.go`:

```go
package fileops

import "testing"

func TestExtractVideoCode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "hyphenated upper", in: "SSNI-083.mp4", want: "SSNI-083", ok: true},
		{name: "hyphenated lower", in: "sone-269.mp4", want: "SONE-269", ok: true},
		{name: "compact padded", in: "ssni00083hhb.mp4", want: "SSNI-083", ok: true},
		{name: "compact no padding", in: "pppd176A.FHD.wmv", want: "PPPD-176", ok: true},
		{name: "compact padded numeric part", in: "soe00967hhb1.wmv", want: "SOE-967", ok: true},
		{name: "technical token rejected", in: "movie.FHD1080.x265.mp4", want: "", ok: false},
		{name: "no code", in: "sample-video.mp4", want: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ExtractVideoCode(tt.in)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("ExtractVideoCode(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestCleanVideoFilenameUsesCompactCode(t *testing.T) {
	got := CleanVideoFilename("ssni00083hhb.mp4")
	if got != "SSNI-083.mp4" {
		t.Fatalf("CleanVideoFilename compact = %q, want SSNI-083.mp4", got)
	}
}
```

- [ ] **Step 2: Run parser tests and verify failure**

Run:

```bash
rtk go test ./internal/fileops -run 'TestExtractVideoCode|TestCleanVideoFilenameUsesCompactCode' -count=1
```

Expected: fail because `ExtractVideoCode` does not exist and compact cleaning is not implemented.

- [ ] **Step 3: Implement parser**

Create `internal/fileops/code.go`:

```go
package fileops

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	hyphenatedCodePattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])([a-z]{2,5})-(\d{3,5})([^a-z0-9]|$)`)
	compactCodePattern   = regexp.MustCompile(`(?i)(^|[^a-z0-9])([a-z]{2,5})0*(\d{3,5})([a-z0-9]*)([^a-z0-9]|$)`)
	technicalPrefixes    = map[string]bool{
		"HD": true, "FHD": true, "UHD": true, "SD": true,
		"AVC": true, "HEVC": true, "XVID": true, "X264": true, "X265": true,
	}
)

// ExtractVideoCode returns a normalized code like SONE-269 from hyphenated or compact names.
func ExtractVideoCode(name string) (string, bool) {
	upper := strings.ToUpper(name)
	if match := hyphenatedCodePattern.FindStringSubmatch(upper); match != nil {
		return normalizeVideoCode(match[2], match[3])
	}
	if match := compactCodePattern.FindStringSubmatch(upper); match != nil {
		prefix := match[2]
		if technicalPrefixes[prefix] {
			return "", false
		}
		return normalizeVideoCode(prefix, match[3])
	}
	return "", false
}

func normalizeVideoCode(prefix, digits string) (string, bool) {
	n, err := strconv.Atoi(digits)
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%s-%03d", strings.ToUpper(prefix), n), true
}
```

Modify `internal/fileops/fileops.go`:

```go
// HasVideoCode checks if a filename contains a valid video code pattern.
// Matches hyphenated codes like SONE-269 and compact codes like sone00269.
func HasVideoCode(filename string) bool {
	_, ok := ExtractVideoCode(filename)
	return ok
}
```

Modify `CleanVideoFilename` in `internal/fileops/fileops.go`:

```go
func CleanVideoFilename(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	code, ok := ExtractVideoCode(filename)
	if !ok {
		return filename
	}
	return code + ext
}
```

Remove the old `codePattern` variable and the now-unused `regexp` import from `internal/fileops/fileops.go`.

- [ ] **Step 4: Run parser tests and package tests**

Run:

```bash
rtk go test ./internal/fileops -run 'TestExtractVideoCode|TestCleanVideoFilenameUsesCompactCode' -count=1
rtk go test ./internal/fileops -count=1
```

Expected: both pass.

- [ ] **Step 5: Commit parser changes**

```bash
rtk git add internal/fileops/code.go internal/fileops/code_test.go internal/fileops/fileops.go
rtk git commit -m "feat: parse compact video codes"
```

---

### Task 2: Add Media Candidate Discovery and Fallback Selection

**Files:**
- Create: `internal/fileops/resolver.go`
- Create: `internal/fileops/resolver_test.go`
- Modify: `internal/fileops/fileops.go`

- [ ] **Step 1: Write failing resolver selection tests**

Create `internal/fileops/resolver_test.go`:

```go
package fileops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveMediaUsesFolderCodeForCompactFile(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "SSNI-083")
	mustMkdir(t, folder)
	video := filepath.Join(folder, "ssni00083hhb.mp4")
	mustWriteSizedFile(t, video, MinVideoSize+1)

	got, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if got.SourcePath != video {
		t.Fatalf("SourcePath = %q, want %q", got.SourcePath, video)
	}
	if got.FileName != "SSNI-083.mp4" {
		t.Fatalf("FileName = %q, want SSNI-083.mp4", got.FileName)
	}
	if got.StagingPath != "" {
		t.Fatalf("StagingPath = %q, want empty for direct file", got.StagingPath)
	}
}

func TestResolveMediaFallsBackToTorrentName(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "download")
	mustMkdir(t, folder)
	video := filepath.Join(folder, "ssni00083hhb.mp4")
	mustWriteSizedFile(t, video, MinVideoSize+1)

	got, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "SSNI-083",
		StagingDir:  filepath.Join(root, "staging"),
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if got.SourcePath != video {
		t.Fatalf("SourcePath = %q, want %q", got.SourcePath, video)
	}
	if got.FileName != "SSNI-083.mp4" {
		t.Fatalf("FileName = %q, want SSNI-083.mp4", got.FileName)
	}
}

func TestResolveMediaRejectsFolderWithoutCode(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "download")
	mustMkdir(t, folder)
	mustWriteSizedFile(t, filepath.Join(folder, "movie.mp4"), MinVideoSize+1)

	_, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "no code here",
		StagingDir:  filepath.Join(root, "staging"),
	})
	if err == nil {
		t.Fatal("ResolveMedia returned nil error, want missing code error")
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteSizedFile(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		t.Fatalf("truncate %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}
```

- [ ] **Step 2: Run resolver tests and verify failure**

Run:

```bash
rtk go test ./internal/fileops -run 'TestResolveMediaUsesFolderCodeForCompactFile|TestResolveMediaFallsBackToTorrentName|TestResolveMediaRejectsFolderWithoutCode' -count=1
```

Expected: fail because `ResolveMedia` and `ResolveRequest` are undefined.

- [ ] **Step 3: Implement candidate discovery and direct selection**

Create `internal/fileops/resolver.go`:

```go
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
```

Modify `internal/fileops/fileops.go` to add `.m2ts` for Blu-ray streams:

```go
".m2ts": true,
```

- [ ] **Step 4: Run resolver tests**

Run:

```bash
rtk go test ./internal/fileops -run 'TestResolveMediaUsesFolderCodeForCompactFile|TestResolveMediaFallsBackToTorrentName|TestResolveMediaRejectsFolderWithoutCode' -count=1
rtk go test ./internal/fileops -count=1
```

Expected: pass.

- [ ] **Step 5: Commit resolver selection**

```bash
rtk git add internal/fileops/resolver.go internal/fileops/resolver_test.go internal/fileops/fileops.go
rtk git commit -m "feat: resolve media with code fallbacks"
```

---

### Task 3: Add Multipart Detection

**Files:**
- Modify: `internal/fileops/resolver.go`
- Modify: `internal/fileops/resolver_test.go`

- [ ] **Step 1: Write failing multipart tests**

Append to `internal/fileops/resolver_test.go`:

```go
func TestFindMultipartSetLetterOrder(t *testing.T) {
	root := t.TempDir()
	files := []string{"pppd176A.FHD.wmv", "pppd176B.FHD.wmv", "pppd176C.FHD.wmv"}
	for _, name := range files {
		mustWriteSizedFile(t, filepath.Join(root, name), MinVideoSize+1)
	}
	videos, _, err := findMediaCandidates(context.Background(), root)
	if err != nil {
		t.Fatalf("findMediaCandidates: %v", err)
	}

	got := findMultipartSet(videos, root, "")
	if len(got) != 3 {
		t.Fatalf("len(parts) = %d, want 3", len(got))
	}
	want := []string{"pppd176A.FHD.wmv", "pppd176B.FHD.wmv", "pppd176C.FHD.wmv"}
	for i := range want {
		if filepath.Base(got[i]) != want[i] {
			t.Fatalf("part %d = %q, want %q", i, filepath.Base(got[i]), want[i])
		}
	}
}

func TestFindMultipartSetNumericOrder(t *testing.T) {
	root := t.TempDir()
	files := []string{"soe00967hhb2.wmv", "soe00967hhb1.wmv"}
	for _, name := range files {
		mustWriteSizedFile(t, filepath.Join(root, name), MinVideoSize+1)
	}
	videos, _, err := findMediaCandidates(context.Background(), root)
	if err != nil {
		t.Fatalf("findMediaCandidates: %v", err)
	}

	got := findMultipartSet(videos, root, "")
	if len(got) != 2 {
		t.Fatalf("len(parts) = %d, want 2", len(got))
	}
	if filepath.Base(got[0]) != "soe00967hhb1.wmv" || filepath.Base(got[1]) != "soe00967hhb2.wmv" {
		t.Fatalf("parts order = %v, want numeric order", got)
	}
}

func TestFindMultipartSetRejectsDuplicateMarkers(t *testing.T) {
	root := t.TempDir()
	files := []string{"soe00967hhb1.wmv", "SOE-967-part1.wmv"}
	for _, name := range files {
		mustWriteSizedFile(t, filepath.Join(root, name), MinVideoSize+1)
	}
	videos, _, err := findMediaCandidates(context.Background(), root)
	if err != nil {
		t.Fatalf("findMediaCandidates: %v", err)
	}

	got := findMultipartSet(videos, root, "")
	if len(got) != 0 {
		t.Fatalf("len(parts) = %d, want 0 for duplicate markers", len(got))
	}
}
```

- [ ] **Step 2: Run multipart tests and verify failure**

Run:

```bash
rtk go test ./internal/fileops -run 'TestFindMultipartSet' -count=1
```

Expected: fail because multipart helpers are missing.

- [ ] **Step 3: Implement multipart helpers**

Do not integrate multipart preparation into `resolveFolder` in this task. Task 3 only adds ordered multipart detection helpers. Task 4 will add `prepareMultipart` and wire detection into the resolver.

Add to `internal/fileops/resolver.go`:

```go
type partCandidate struct {
	path  string
	code  string
	order int
}

var (
	partWordPattern     = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(part|cd|disc)0*([1-9][0-9]*)(?:[^a-z0-9]|$)`)
	trailingNumberPart  = regexp.MustCompile(`(?i)([a-z]+)0*\d{3,5}[a-z]*([1-9][0-9]*)$`)
	trailingLetterPart  = regexp.MustCompile(`(?i)([a-z]+)0*\d{3,5}([a-z])(?:[^a-z0-9].*)?$`)
)

func findMultipartSet(videos []mediaCandidate, folder, torrentName string) []string {
	if len(videos) < 2 {
		return nil
	}

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
		groups[code] = append(groups[code], partCandidate{path: video.Path, code: code, order: order})
	}

	for _, parts := range groups {
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
	for _, part := range parts {
		if seen[part.order] {
			return false
		}
		seen[part.order] = true
	}
	return true
}
```

Add missing imports to `resolver.go`: `regexp` and `strconv`.

- [ ] **Step 4: Run multipart and resolver tests**

Run:

```bash
rtk go test ./internal/fileops -run 'TestFindMultipartSet|TestResolveMedia' -count=1
rtk go test ./internal/fileops -count=1
```

Expected: pass.

- [ ] **Step 5: Commit multipart detection**

```bash
rtk git add internal/fileops/resolver.go internal/fileops/resolver_test.go
rtk git commit -m "feat: detect ordered multipart videos"
```

---

### Task 4: Add Media Preparation Commands

**Files:**
- Create: `internal/fileops/preparer.go`
- Create: `internal/fileops/preparer_test.go`
- Modify: `internal/fileops/resolver.go`
- Modify: `internal/fileops/resolver_test.go`

- [ ] **Step 1: Write failing command construction tests**

Create `internal/fileops/preparer_test.go`:

```go
package fileops

import (
	"context"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls []string
	err   error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return f.err
}

func TestConcatVideosUsesFFmpegCopyFirst(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}
	out := root + "/SSNI-083.mkv"

	prepared, err := concatVideos(context.Background(), []string{root + "/a.wmv", root + "/b.wmv"}, out, runner)
	if err != nil {
		t.Fatalf("concatVideos returned error: %v", err)
	}
	if prepared != out {
		t.Fatalf("prepared path = %q, want %q", prepared, out)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(runner.calls))
	}
	if !strings.Contains(runner.calls[0], "ffmpeg -y -f concat -safe 0 -i") {
		t.Fatalf("ffmpeg concat call missing expected args: %s", runner.calls[0])
	}
	if !strings.Contains(runner.calls[0], "-c copy "+out) {
		t.Fatalf("ffmpeg concat call missing stream copy output: %s", runner.calls[0])
	}
}

func TestExtractISOUsesBsdtar(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}

	err := extractImage(context.Background(), root+"/movie.iso", root+"/extract", runner)
	if err != nil {
		t.Fatalf("extractImage returned error: %v", err)
	}
	if len(runner.calls) == 0 || !strings.HasPrefix(runner.calls[0], "bsdtar -xf ") {
		t.Fatalf("first call = %v, want bsdtar extraction", runner.calls)
	}
}
```

- [ ] **Step 2: Run command tests and verify failure**

Run:

```bash
rtk go test ./internal/fileops -run 'TestConcatVideosUsesFFmpegCopyFirst|TestExtractISOUsesBsdtar' -count=1
```

Expected: fail because preparation helpers are missing.

- [ ] **Step 3: Implement command runner and preparation helpers**

Modify `ResolveRequest` in `internal/fileops/resolver.go` to add the command runner:

```go
type ResolveRequest struct {
	Context     context.Context
	Path        string
	TorrentName string
	StagingDir  string
	Runner      CommandRunner
}
```

Modify `ResolveMedia` in `internal/fileops/resolver.go` to default the runner:

```go
	if req.Runner == nil {
		req.Runner = ExecCommandRunner{}
	}
```

Place that block after the existing context defaulting.

Modify `resolveFolder` in `internal/fileops/resolver.go` so multipart candidates are prepared before single-video fallback:

```go
	if parts := findMultipartSet(videos, req.Path, req.TorrentName); len(parts) > 1 {
		return prepareMultipart(req, parts)
	}
```

Place that block after `findMediaCandidates` succeeds and before `bestVideoCandidate`.

Create `internal/fileops/preparer.go`:

```go
package fileops

import (
	"context"
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
	code, ok := bestCodeFor(parts[0], req.TorrentName)
	if !ok {
		return nil, fmt.Errorf("no code found for multipart video")
	}
	out := filepath.Join(req.StagingDir, code+".mkv")
	prepared, err := concatVideos(req.Context, parts, out, req.Runner)
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

func concatVideos(ctx context.Context, parts []string, out string, runner CommandRunner) (string, error) {
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}
	listPath := filepath.Join(filepath.Dir(out), strings.TrimSuffix(filepath.Base(out), filepath.Ext(out))+".concat.txt")
	if err := writeConcatList(listPath, parts); err != nil {
		return "", err
	}
	if err := runner.Run(ctx, "ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", listPath, "-c", "copy", out); err != nil {
		mp4Out := ChangeExtension(out, ".mp4")
		if transcodeErr := runner.Run(ctx, "ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", listPath, "-c:v", "libx264", "-c:a", "aac", mp4Out); transcodeErr != nil {
			return "", fmt.Errorf("concat copy failed: %v; transcode failed: %w", err, transcodeErr)
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

func extractImage(ctx context.Context, imagePath, outDir string, runner CommandRunner) error {
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
		isoPath := ChangeExtension(imagePath, ".iso")
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
```

- [ ] **Step 4: Run preparation tests**

Run:

```bash
rtk go test ./internal/fileops -run 'TestConcatVideosUsesFFmpegCopyFirst|TestExtractISOUsesBsdtar' -count=1
rtk go test ./internal/fileops -count=1
```

Expected: pass.

- [ ] **Step 5: Commit preparation commands**

```bash
rtk git add internal/fileops/preparer.go internal/fileops/preparer_test.go internal/fileops/resolver.go internal/fileops/resolver_test.go
rtk git commit -m "feat: prepare multipart media"
```

---

### Task 5: Add Disc Image Resolution Over Extracted Media

**Files:**
- Modify: `internal/fileops/resolver.go`
- Modify: `internal/fileops/resolver_test.go`
- Modify: `internal/fileops/preparer.go`

- [ ] **Step 1: Write failing image resolution tests**

Append to `internal/fileops/resolver_test.go`:

```go
func TestResolveMediaPreparesImageToStaging(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "SSNI-083.iso")
	mustWriteSizedFile(t, image, 1024)
	staging := filepath.Join(root, "staging")
	runner := &fakeRunner{}

	got, err := ResolveMedia(ResolveRequest{
		Path:        image,
		TorrentName: "SSNI-083",
		StagingDir:  staging,
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if got.FileName != "SSNI-083.mkv" {
		t.Fatalf("FileName = %q, want SSNI-083.mkv", got.FileName)
	}
	if got.StagingPath != filepath.Join(staging, "SSNI-083.mkv") {
		t.Fatalf("StagingPath = %q, want prepared staging mkv", got.StagingPath)
	}
}

func TestIsImageFileRecognizesDiscImageExtensions(t *testing.T) {
	for _, path := range []string{"movie.iso", "movie.nrg", "movie.img", "movie.mdf", "movie.bin"} {
		if !IsImageFile(path) {
			t.Fatalf("IsImageFile(%q) = false, want true", path)
		}
	}
	if IsImageFile("movie.txt") {
		t.Fatal("IsImageFile(movie.txt) = true, want false")
	}
}
```

- [ ] **Step 2: Run image resolution test and verify failure**

Run:

```bash
rtk go test ./internal/fileops -run TestResolveMediaPreparesImageToStaging -count=1
```

Expected: fail because `prepareImage` is not implemented.

- [ ] **Step 3: Implement image preparation resolution**

Add to `internal/fileops/preparer.go`:

```go
func prepareImage(req ResolveRequest, imagePath string) (*ResolvedMedia, error) {
	code, ok := bestCodeFor(imagePath, req.TorrentName)
	if !ok {
		return nil, fmt.Errorf("no code found in image filename, folder, or torrent name")
	}

	extractDir := filepath.Join(req.StagingDir, code+"-image")
	if err := extractImage(req.Context, imagePath, extractDir, req.Runner); err != nil {
		return nil, fmt.Errorf("image extraction failed: %w", err)
	}

	out := filepath.Join(req.StagingDir, code+".mkv")
	parts, err := selectExtractedMedia(extractDir)
	if err != nil {
		return nil, err
	}
	prepared := out
	if len(parts) == 1 {
		var err error
		prepared, err = remuxVideo(req.Context, parts[0], out, req.Runner)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		prepared, err = concatVideos(req.Context, parts, out, req.Runner)
		if err != nil {
			return nil, err
		}
	}

	return &ResolvedMedia{
		SourcePath:         prepared,
		FileName:           filepath.Base(prepared),
		StagingPath:        prepared,
		Code:               code,
		HasChineseSubtitle: HasChineseSubtitle(filepath.Base(imagePath)),
	}, nil
}

func remuxVideo(ctx context.Context, in, out string, runner CommandRunner) (string, error) {
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}
	if err := runner.Run(ctx, "ffmpeg", "-y", "-i", in, "-map", "0:v:0", "-map", "0:a?", "-c", "copy", out); err != nil {
		mp4Out := ChangeExtension(out, ".mp4")
		if transcodeErr := runner.Run(ctx, "ffmpeg", "-y", "-i", in, "-map", "0:v:0", "-map", "0:a?", "-c:v", "libx264", "-c:a", "aac", mp4Out); transcodeErr != nil {
			return "", fmt.Errorf("remux failed: %v; transcode failed: %w", err, transcodeErr)
		}
		return mp4Out, nil
	}
	return out, nil
}
```

Add to `internal/fileops/resolver.go`:

```go
func selectExtractedMedia(dir string) ([]string, error) {
	if parts := selectDVDTitleChain(dir); len(parts) > 0 {
		return parts, nil
	}
	if stream := selectLargestByGlob(filepath.Join(dir, "BDMV", "STREAM", "*.m2ts")); stream != "" {
		return []string{stream}, nil
	}
	videos, _, err := findMediaCandidates(dir)
	if err != nil {
		return nil, err
	}
	if parts := findMultipartSet(videos, dir, ""); len(parts) > 1 {
		return parts, nil
	}
	best := bestVideoCandidate(videos, dir, "")
	if best != nil {
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
		if len(name) >= len("VTS_01_1.VOB") {
			groups[name[:6]] = append(groups[name[:6]], path)
		}
	}
	var best []string
	var bestSize int64
	for _, group := range groups {
		sort.Strings(group)
		var size int64
		for _, path := range group {
			if info, err := os.Stat(path); err == nil {
				size += info.Size()
			}
		}
		if size > bestSize {
			best = group
			bestSize = size
		}
	}
	return best
}

func selectLargestByGlob(pattern string) string {
	matches, _ := filepath.Glob(pattern)
	var best string
	var bestSize int64
	for _, path := range matches {
		if info, err := os.Stat(path); err == nil && info.Size() > bestSize {
			best = path
			bestSize = info.Size()
		}
	}
	return best
}
```

Update the imports in `internal/fileops/preparer_test.go`:

```go
import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)
```

Update `fakeRunner.Run` in `internal/fileops/preparer_test.go` so when it sees `bsdtar` or `7z`, it creates a small fake Blu-ray stream under the requested extraction directory:

```go
func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name == "bsdtar" || name == "7z" {
		outDir := args[len(args)-1]
		if strings.HasPrefix(outDir, "-o") {
			outDir = strings.TrimPrefix(outDir, "-o")
		}
		stream := filepath.Join(outDir, "BDMV", "STREAM", "00001.m2ts")
		_ = os.MkdirAll(filepath.Dir(stream), 0755)
		_ = os.WriteFile(stream, []byte("media"), 0644)
	}
	return f.err
}
```

- [ ] **Step 4: Run image tests**

Run:

```bash
rtk go test ./internal/fileops -run 'TestResolveMediaPreparesImageToStaging|TestIsImageFileRecognizesDiscImageExtensions|TestExtractISOUsesBsdtar' -count=1
rtk go test ./internal/fileops -count=1
```

Expected: pass.

- [ ] **Step 5: Commit image resolution**

```bash
rtk git add internal/fileops/resolver.go internal/fileops/resolver_test.go internal/fileops/preparer.go internal/fileops/preparer_test.go
rtk git commit -m "feat: resolve disc image media"
```

---

### Task 6: Integrate Resolver in Torrent Handler

**Files:**
- Modify: `internal/handler/handler.go`

- [ ] **Step 1: Update handler to use resolver**

Replace the media selection block in `TorrentComplete` with:

```go
	resolved, err := fileops.ResolveMedia(fileops.ResolveRequest{
		Context:     c.Request.Context(),
		Path:        req.Path,
		TorrentName: req.Name,
		StagingDir:  h.folders.Staging,
	})
	if err != nil {
		if fileops.Exists(req.Path) {
			logger.Warnf("⚠️ %v in: %s", err, req.Path)
			c.JSON(http.StatusOK, gin.H{
				"message": "no valid video files found",
				"jobs":    []string{},
			})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "path does not exist"})
		return
	}

	jobID := uuid.New().String()[:8]
	fileName := resolved.FileName

	isLight := resolved.HasChineseSubtitle

	job := queue.NewJob(jobID, resolved.SourcePath, fileName, req.Name, req.Category)
	job.IsLight = isLight
	job.StagingPath = resolved.StagingPath
```

Remove the old `var videoPath string` block and `fileName := filepath.Base(videoPath)`. Keep `path/filepath` imported because the rest of `handler.go` uses it in retry/list endpoints.

- [ ] **Step 2: Run handler package tests**

Run:

```bash
rtk go test ./internal/handler -count=1
rtk go test ./... -count=1
```

Expected: pass.

- [ ] **Step 3: Commit handler integration**

```bash
rtk git add internal/handler/handler.go
rtk git commit -m "feat: use media resolver for torrent webhooks"
```

---

### Task 7: Add Runtime Dependencies and Docs

**Files:**
- Modify: `Dockerfile`
- Modify: `README.md`

- [ ] **Step 1: Update Dockerfile dependencies**

Modify the runtime `apt-get install` block in `Dockerfile`:

```dockerfile
RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg \
    curl \
    ca-certificates \
    tzdata \
    libgomp1 \
    fonts-noto-cjk \
    libarchive-tools \
    7zip \
    nrg2iso \
    && rm -rf /var/lib/apt/lists/*
```

- [ ] **Step 2: Document new detection behavior**

Add to `README.md` under `## Video File Filtering`:

```markdown
Fusionn-Muse detects codes from both hyphenated and compact filenames:

- `SSNI-083.mp4` -> `SSNI-083`
- `ssni00083hhb.mp4` -> `SSNI-083`
- `pppd176A.FHD.wmv` -> `PPPD-176`

When a video filename does not contain a usable code, folder name is checked next, then torrent name.

Ordered multipart videos such as `ABC-001A.wmv`, `ABC-001B.wmv`, or `abc00001hhb1.wmv`, `abc00001hhb2.wmv` are assembled into one `.mkv` before processing. Playable disc/archive image sources such as `.iso`, `.nrg`, `.img`, `.mdf`, and `.bin` are extracted without Docker loop mounts and remuxed to `.mkv` when possible.
```

- [ ] **Step 3: Verify package tests**

Run:

```bash
rtk go test ./... -count=1
```

Expected: pass.

- [ ] **Step 4: Commit runtime/docs**

```bash
rtk git add Dockerfile README.md
rtk git commit -m "docs: describe media resolution support"
```

---

### Task 8: Final Verification

**Files:**
- No new code files unless verification exposes failures.

- [ ] **Step 1: Run full Go tests**

Run:

```bash
rtk go test ./... -count=1
```

Expected: pass.

- [ ] **Step 2: Run Go formatting**

Run:

```bash
rtk gofmt -w internal/fileops/code.go internal/fileops/code_test.go internal/fileops/resolver.go internal/fileops/resolver_test.go internal/fileops/preparer.go internal/fileops/preparer_test.go internal/fileops/fileops.go internal/handler/handler.go
rtk go test ./... -count=1
```

Expected: gofmt produces no semantic changes and tests pass.

- [ ] **Step 3: Check final diff**

Run:

```bash
rtk git status --short
rtk git log --oneline -5
```

Expected: only intended files changed or committed. The pre-existing untracked `.codegraph/` directory may still be present and should not be committed unless the user asks.

- [ ] **Step 4: Manual smoke scenario**

Create a temporary folder outside tracked source and run a small resolver-only Go test or package test using these cases:

- folder `SSNI-083/ssni00083hhb.mp4` with file size greater than `MinVideoSize`
- folder `parts/pppd176A.FHD.wmv`, `parts/pppd176B.FHD.wmv`
- direct file `SSNI-083.iso` with fake runner in tests

Expected:

- first resolves to direct source and `SSNI-083.mp4`
- second resolves to prepared staging `PPPD-176.mkv`
- third resolves to prepared staging `SSNI-083.mkv`

Do not add large binary media fixtures to the repo.

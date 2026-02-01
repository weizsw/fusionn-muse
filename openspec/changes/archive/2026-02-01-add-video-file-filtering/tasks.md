# Tasks: add-video-file-filtering

## 1. Implementation

- [x] 1.1 Add `MinVideoSize` constant (200MB) to `fileops.go`
- [x] 1.2 Add `HasVideoCode(filename string) bool` function to check if filename contains valid code pattern
- [x] 1.3 Add `FindValidVideoFile(dir string) (string, error)` that:
  - Finds all video files recursively
  - Filters by code pattern match
  - Filters by size > 200MB
  - Returns the largest matching file (or error if none found)
- [x] 1.4 Update `handler.TorrentComplete` to use `FindValidVideoFile` for folder paths instead of `FindVideoFiles`
- [x] 1.5 Add logging for skipped files (debug level) to aid troubleshooting

## 2. Verification

- [x] 2.1 Test with folder containing: main video (>200MB with code), ad video (<50MB), sample video (no code pattern)
- [x] 2.2 Test with single video file (bypass folder filtering)
- [x] 2.3 Test with folder containing no valid videos (should return error)

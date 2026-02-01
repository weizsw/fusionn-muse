# Change: Add video file filtering for torrent folders

## Why
When a torrent downloads as a folder, it often contains multiple video files—ads, trailers, samples, or bonus content. Currently, the handler queues ALL video files found, which wastes processing time and resources on unwanted files. Each torrent should produce exactly one valid video file for processing.

## What Changes
- Filter video files by **name pattern**: Must contain a code matching `[A-Z]{2,5}-\d{3,5}` (e.g., `SONE-269`, `JUR-123`). Handles prefixed filenames like `123SONE-269.mp4`.
- Filter by **minimum file size**: Files ≤200MB are rejected (ads/samples are typically small).
- **Select largest** when multiple files match both criteria.
- Update `FindVideoFiles` in `fileops` to support filtering, or add a new `FindValidVideoFile` function.

## Impact
- Affected code: `internal/fileops/fileops.go`, `internal/handler/handler.go`
- No breaking changes to API or config
- Reduces false positives in processing queue


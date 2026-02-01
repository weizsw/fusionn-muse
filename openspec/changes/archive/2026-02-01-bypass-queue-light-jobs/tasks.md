# Tasks: Bypass Queue for Light Jobs

## Implementation Tasks

### T1: Add Job Type Detection to Handler

**File:** `internal/handler/handler.go`

- [x] **T1.1** Import `fileops` package (already imported)
- [x] **T1.2** After determining `videoPath`, call `fileops.HasChineseSubtitle(filepath.Base(videoPath))`
- [x] **T1.3** If Chinese detected → call `processLightJob()` in goroutine, respond immediately
- [x] **T1.4** If not detected → queue as usual (existing behavior)

### T2: Add Light Job Processor

**File:** `internal/handler/handler.go`

- [x] **T2.1** Create `processLightJob(job *queue.Job)` function
- [x] **T2.2** Implement light processing:
  - Stage file (hardlink/copy)
  - Clean filename
  - Move directly to scraping (skip transcribe/translate)
  - Log completion
- [x] **T2.3** Handle errors gracefully (move to failed folder)

### T3: Add Job Type Field

**File:** `internal/queue/job.go`

- [x] **T3.1** Add `IsLight bool` field to `Job` struct
- [x] **T3.2** Set field when creating job in handler

### T4: Update Queue Stats

**File:** `internal/queue/queue.go`

- [x] **T4.1** Track light jobs separately (even though they bypass queue)
- [x] **T4.2** Add `light_completed` and `light_failed` counters to stats
- [x] **T4.3** Add `RegisterLightJob()`, `MarkLightJobCompleted()`, `MarkLightJobFailed()` methods

### T5: Build Verification

- [x] **T5.1** Code compiles successfully with `go build ./...`

## Code Changes Summary

```
internal/handler/handler.go
├── TorrentComplete() - Add Chinese detection and branching
├── processLightJob() - New function for light jobs
└── handleLightJobError() - New function for error handling

internal/queue/job.go
└── Job struct - Added IsLight field

internal/queue/queue.go
├── Queue struct - Added lightCompleted, lightFailed counters
├── GetQueueStats() - Added light_completed, light_failed to stats
├── RegisterLightJob() - New method to register light jobs
├── MarkLightJobCompleted() - New method
└── MarkLightJobFailed() - New method
```

## Dependencies

- `fileops.HasChineseSubtitle()` - Already exists and working ✅
- `fileops.HardlinkOrCopy()` - Already exists ✅
- `fileops.Move()` - Already exists ✅
- `fileops.CleanVideoFilename()` - Already exists ✅

## Estimated Effort

- T1: 30 min ✅
- T2: 1 hour ✅
- T3: 15 min ✅
- T4: 30 min ✅
- T5: 5 min ✅

**Total: ~2.5 hours** → Completed

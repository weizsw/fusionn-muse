# Design: Bypass Queue for Light Jobs

## Architecture

### Current Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                           Handler                                │
│  TorrentComplete() → Create Job → Queue.Enqueue()               │
└─────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────┐
│                            Queue                                 │
│  jobsChan → worker() → processor.Process() → Complete           │
│                    (single worker, sequential)                   │
└─────────────────────────────────────────────────────────────────┘
```

### Proposed Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                           Handler                                │
│  TorrentComplete() → Detect Chinese?                            │
│                          │                                       │
│                          ├─ YES → go processLightJob()          │
│                          │        (immediate, parallel)          │
│                          │                                       │
│                          └─ NO → Queue.Enqueue()                │
│                                   (sequential)                   │
└─────────────────────────────────────────────────────────────────┘
                                │
                ┌───────────────┴───────────────┐
                │                               │
                ▼                               ▼
┌───────────────────────┐       ┌───────────────────────────────┐
│   Light Processing    │       │           Queue               │
│   (goroutine)         │       │   worker() → processor.Process│
│                       │       │   (single worker)             │
│   - Stage             │       │                               │
│   - Clean filename    │       │   - Stage                     │
│   - Move to scraping  │       │   - Clean filename            │
│   (skip transcribe)   │       │   - Transcribe                │
└───────────────────────┘       │   - Translate                 │
                                │   - Move to scraping          │
                                └───────────────────────────────┘
```

## Data Flow

### Light Job Flow

```
1. Webhook receives: /data/torrents/SSIS-127_C.mp4
2. Handler detects: HasChineseSubtitle("SSIS-127_C.mp4") → true
3. Handler spawns: go processLightJob(job)
4. Responds: 202 Accepted {job: "abc123", type: "light"}
5. Light processor:
   a. Stage: hardlink → /data/automation/staging/SSIS-127_C.mp4
   b. Clean: SSIS-127_C.mp4 → SSIS-127.mp4
   c. Move: → /data/automation/scraping/SSIS-127.mp4
   d. Notify: success
6. Done (~1 second)
```

### Heavy Job Flow (unchanged)

```
1. Webhook receives: /data/torrents/SSIS-128.mp4
2. Handler detects: HasChineseSubtitle("SSIS-128.mp4") → false
3. Handler queues: Queue.Enqueue(job)
4. Responds: 202 Accepted {job: "def456", type: "heavy"}
5. Queue worker processes (when ready):
   a. Stage → b. Clean → c. Transcribe → d. Translate → e. Move
6. Done (~10-30 minutes)
```

## Concurrency Safety

### Potential Race Conditions

1. **Same file queued twice** → Existing duplicate detection (not changed)
2. **Staging folder conflicts** → Each job has unique staging path
3. **Processing folder conflicts** → Light jobs skip processing folder

### Thread Safety

- Light jobs only touch: staging → scraping (no processing folder)
- Heavy jobs touch: staging → processing → scraping
- No overlap in folders during active processing

## Handler Changes (Pseudocode)

```go
func (h *Handler) TorrentComplete(c *gin.Context) {
    // ... existing validation ...
    
    jobID := uuid.New().String()[:8]
    fileName := filepath.Base(videoPath)
    
    // NEW: Detect job type
    isLight := fileops.HasChineseSubtitle(fileName)
    
    job := queue.NewJob(jobID, videoPath, fileName, req.Name, req.Category)
    job.IsLight = isLight
    
    if isLight {
        // Process immediately in background
        go h.processLightJob(job)
        
        c.JSON(http.StatusAccepted, gin.H{
            "message": "light job started",
            "job":     jobID,
            "type":    "light",
        })
    } else {
        // Queue for sequential processing
        h.queue.Enqueue(job)
        
        c.JSON(http.StatusAccepted, gin.H{
            "message": "job queued",
            "job":     jobID,
            "type":    "heavy",
        })
    }
}

func (h *Handler) processLightJob(job *queue.Job) {
    // Stage
    stagingPath := filepath.Join(h.folders.Staging, job.FileName)
    if err := fileops.HardlinkOrCopy(job.SourcePath, stagingPath); err != nil {
        h.handleLightError(job, err)
        return
    }
    
    // Clean filename
    cleanedName := fileops.CleanVideoFilename(job.FileName)
    
    // Move directly to scraping (skip processing folder)
    scrapingPath := filepath.Join(h.folders.Scraping, cleanedName)
    if err := fileops.Move(stagingPath, scrapingPath); err != nil {
        h.handleLightError(job, err)
        return
    }
    
    // Notify success
    logger.Infof("✅ Light job completed: %s", cleanedName)
}
```

## Alternatives Considered

### Option A: Two Queues (Rejected)

- Heavy queue (1 worker) + Light queue (unlimited workers)
- **Rejected**: Over-engineered, light jobs don't need queue overhead

### Option B: Priority Queue (Rejected)

- Single queue with priority levels
- **Rejected**: Still sequential, light jobs would still wait

### Option C: Early Detection in Handler (Selected)

- Detect at webhook, bypass queue entirely for light jobs
- **Selected**: Simple, effective, minimal code change


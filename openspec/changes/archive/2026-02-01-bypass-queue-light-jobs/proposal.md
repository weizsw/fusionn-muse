# Proposal: Bypass Queue for Light Jobs

## Summary

Allow "light" jobs (videos with Chinese subtitles already detected) to process immediately without waiting in the queue, while "heavy" jobs (requiring transcription + translation) continue to queue sequentially.

## Problem

Currently:
1. All jobs enter a single sequential queue
2. A quick "light" job (just file moves, no LLM) must wait behind slow "heavy" jobs (transcription + translation can take 10-30 minutes)
3. This creates unnecessary delays for videos that already have Chinese subtitles

## Solution

**Detect job type at webhook time**, then:
- **Light jobs** → Process immediately in a goroutine (no queue)
- **Heavy jobs** → Queue as usual (single worker, sequential)

### Job Classification

| Job Type | Condition | Processing |
|----------|-----------|------------|
| **Light** | `HasChineseSubtitle(filename) == true` | Immediate (parallel) |
| **Heavy** | `HasChineseSubtitle(filename) == false` | Queue (sequential) |

### Light Job Processing

Light jobs only need:
1. Stage file (hardlink/copy)
2. Clean filename
3. Move to processing
4. Move to scraping (skip transcribe/translate)

These are pure file operations (~1 second), safe to run in parallel.

## Current Flow

```
Webhook → Queue → Single Worker → Process (detect Chinese inside)
                      ↓
              Wait for heavy jobs...
```

## Proposed Flow

```
Webhook → Detect Chinese?
              │
              ├─ YES (Light) → goroutine → Process immediately
              │
              └─ NO (Heavy) → Queue → Single Worker → Process
```

## Benefits

1. **No blocking**: Light jobs don't wait for heavy jobs
2. **Resource efficient**: Only heavy jobs compete for LLM/Whisper resources
3. **Simple change**: Detection already exists, just move it earlier

## Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| Race condition on file ops | Use unique staging paths per job |
| Too many parallel light jobs | File ops are fast (~1s), unlikely to overwhelm |
| Incorrect detection | Existing `HasChineseSubtitle` is well-tested |

## Acceptance Criteria

1. Light jobs complete immediately without waiting for heavy jobs in queue
2. Heavy jobs still process one at a time (no concurrent transcription)
3. Queue stats still track both job types
4. No race conditions or file corruption


# Durable job processing

Status: Accepted

## Goals

- Deduplicate every submission of the same media before work is queued.
- Retry Translation without repeating a completed Transcription.
- Persist source-language Transcriptions so an operator can edit and Retranslate them.
- Preserve queued work and complete Job history across service restarts.

## Identity and admission

A readable Media item is identified primarily by a strong sampled-content signature derived from its size plus the first and last 1 MiB. The filename returned by `mediaintake.CleanVideoFilename`, including the cleaned extension and ignoring its source directory, is stored as a fallback source key and alias.

Content signatures make copies and renamed files converge on one Job while allowing distinct files with the same cleaned filename to remain separate. If the file cannot be read, the normalized filename is used conservatively as the identity. Existing filename-only records migrate into fallback aliases. Database uniqueness constraints, not in-memory checks, arbitrate concurrent submissions.

New Job admission uses one reservation operation for the torrent completion webhook and `/api/v1/retry/staging`. Operator Job actions and failed-folder retry routes instead create later Attempts for an existing Job.

A Duplicate submission is an idempotent success. It returns the existing Job ID and does not change queue position or create an Attempt. This remains true for completed, failed, interrupted, queued, and running Jobs. Reprocessing requires an explicit Job action.

The torrent webhook reserves the Job and initial Attempt in SQLite before acknowledging the request. It then immediately hardlinks the source into staging, using the existing copy fallback. The Attempt becomes claimable only after staging succeeds and its checkpoint is stored. If both operations fail, the initial Attempt fails, the webhook returns failure, and the Job reservation remains so later submissions stay deduplicated.

`/api/v1/retry/staging` admits an untracked staged file as a new Job. A tracked file is reported as a Duplicate and left untouched. An explicit Manual retry may adopt the one uniquely matching staged file when the Job's recorded media path is missing.

## Durable records

Use a local SQLite database at `/data/automation/meta/fusionn-muse.db` through the pure-Go `modernc.org/sqlite` driver. The service remains a `CGO_ENABLED=0` static build. Database open, initialization, or migration failure prevents startup; there is no non-durable fallback.

SQLite stores:

- one Job per content signature, with normalized filename fallback keys;
- every Attempt and its parent Job ID;
- queued, running, terminal, and interrupted state;
- Attempt start and end timestamps, outcome, failure stage, and error text;
- durable Stage checkpoints and known artifact paths; and
- each Attempt's non-secret settings snapshot.

Settings snapshots include the provider, model, prompts, and relevant processing settings. They never contain API keys or other credentials. Raw application logs remain in files and are not copied into SQLite.

Queued Attempt rows are the authoritative Queue. Workers claim work directly from SQLite; an in-memory signal may wake workers but is never queue state. Job and Attempt history has no automatic retention limit.

Only one fusionn-muse service instance may run against this database. Cross-process leases and distributed concurrency are out of scope.

## Attempt and stage behavior

The pipeline records a checkpoint immediately after each stage succeeds. At minimum, recovery distinguishes preparation, move to processing, Transcription persistence, Translation publication, and video delivery. Recovery trusts a checkpoint plus the presence and readability of its required artifact; no content hash is required.

A normal execution keeps the same durable Attempt while it hands work from preparation to Transcription, Translation, and delivery. The handoff persists the next current stage and returns the Attempt to pending state; it does not create another Attempt. A later Attempt is created only for an automatic Translation retry, Manual retry, Resume, or Retranslation.

If a filesystem move succeeds but the process stops before its checkpoint commit, Resume reconciles known automation folders. It accepts recovery only when exactly one expected location proves the completed move. Missing or conflicting files fail explicitly.

A Job that has Sufficient Chinese subtitle coverage completes without Transcription or Translation. An older sidecar subtitle alone does not qualify and is not reused as the persisted Transcription.

A failure before Transcription persistence fails the Job immediately. A Transcription command that succeeds but cannot persist its artifact also fails the Job immediately. A terminal Transcription failure or exhausted Translation failure moves unfinished media from `processing` to `failed`. An intermediate Translation failure leaves media in `processing` for the automatic retry. A failed Retranslation of an already completed Job leaves its delivered media untouched.

## Persisted Transcriptions

Persist each source-language Transcription as an SRT file in `/data/automation/transcriptions`. Its basename matches the translated subtitle output:

```text
SNOS-123.mp4 -> /data/automation/transcriptions/SNOS-123.srt
```

Write the file through a temporary file and atomic rename. Do not store a second database copy or a content hash. The persisted file is the source of truth, and deliberate operator edits become the input to the next Translation.

A Translation retry, Retranslation, or Resume that requires a missing or unreadable Transcription fails explicitly. It never silently retranscribes.

A completed Job may be Retranslated when its original video is gone. Media presence is required only when an unfinished Job still needs video delivery.

## Translation retry

A Translation failure creates a new Attempt under the same Job and starts at Translation from the persisted Transcription. It never repeats Transcription.

Allow three automatic retries after the first translation attempt, for at most four translation attempts. Wait 10 seconds between attempts. The retry wait is persisted outside the Translation lane; it does not reserve a worker, and another eligible Translation may run during the delay.

Automatic Translation retries use the original accepted non-secret settings snapshot and the current credential. Successfully cached translation chunks from the failed Attempt may be reused. Every failed Attempt is logged and recorded, but send the error notification only after all retries are exhausted.

The same automatic retry policy applies when a manually requested Retranslation encounters a Translation failure.

## Manual retry, Resume, and Retranslation

A Manual retry applies to a terminally failed Job. It creates an Attempt at the first incomplete stage and uses the Job's original accepted settings. It may adopt the uniquely matching staged file when the Job's recorded media path is missing. The failed-folder compatibility routes provide the same action after safely moving matched media directly from `failed` to `processing`.

A Resume applies only to an Interrupted Job. It creates an Attempt at the first incomplete stage under the same Job ID.

A Retranslation applies to a terminal Job with a valid persisted Transcription. It starts at Translation, bypasses all existing translation-result cache entries, and may populate the cache only after successful Translation. It synchronously reloads and validates the configuration file before creating the Attempt, then snapshots the latest non-secret settings. An invalid config rejects the request without creating an Attempt. Normal admissions and Manual retries do not force a reload.

Retranslation writes to a temporary output and atomically replaces the translated subtitle only after success. Failure preserves any prior good subtitle. A completed Job remains completed while its Retranslation is queued or running and also remains completed if Retranslation fails; the active or failed Attempt is exposed separately. A successful Retranslation of a translation-failed Job continues delivery and moves its video from `failed` to `scraping`.

Operator actions are idempotent while any Attempt for that Job is active: return the Job ID and active Attempt ID without creating another Attempt.

## Restart and shutdown

On startup, Attempts that were queued but never started remain queued and are eligible for automatic claim. Attempts that were running, and their unfinished Jobs, become Interrupted and require an explicit Resume.

On graceful shutdown, stop claiming work, cancel the running external command, mark its Attempt and unfinished Job Interrupted, and leave queued Attempts queued.

## Execution lanes

Remove the current hard-coded 100-heavy-job admission limit. SQLite may retain any number of queued Attempts.

Use independent FIFO lanes for Transcription and Translation. At most one Transcription and one Translation run at a time, and those two operations may overlap. A stage-aware claim preserves FIFO order within each lane.

Preparation, file movement, delivery, and pre-detected light work use concurrent orchestration outside both heavy lanes. They do not occupy a Transcription or Translation slot.

## HTTP API

Keep the HTTP API unauthenticated because it is deployed only on a trusted private network.

Preserve `/api/v1/queue`, `/api/v1/queue/stats`, and `/api/v1/queue/:id` as compatibility views of active work. Add:

- `GET /api/v1/jobs` for newest-first history, using cursor pagination with a default of 50 and maximum of 200 records;
- `GET /api/v1/jobs/:id` for Job detail and Attempt history;
- `POST /api/v1/jobs/:id/retry` for a failed Job;
- `POST /api/v1/jobs/:id/resume` for an Interrupted Job; and
- `POST /api/v1/jobs/:id/retranslate` for an eligible terminal Job with a persisted Transcription.

A wrong action returns `409 Conflict` without performing another action implicitly. The response includes `code: "invalid_job_action"`, the Job ID, current status, `allowed_actions`, and the correct action URL.

Preserve `/api/v1/retry/staging`, `/api/v1/retry/failed`, and `/api/v1/retry/failed/:name` as compatibility shims. Staging admits only untracked media. Failed-file routes match a persisted failed Job, skip orphans and Jobs with an active or non-failed status, create the `processing/<job.FileName>` destination without replacement, remove the failed source, and call `RetryFrom` with the existing Job ID. If durable Attempt creation fails, restore the media to `failed`. A same-name unrelated processing file is never overwritten. Bulk routes continue after per-file errors and preserve accepted, skipped, and failed results plus partial-success status behavior.

## Rollout

The first SQLite deployment starts with empty Job history and must happen while no work is in flight. Do not infer historical Jobs from existing folders or subtitle outputs.

The active deployment bind-mounts `/Volumes/External:/data`. Add `/data/automation/transcriptions` and `/data/automation/meta`. The repository's current `docker-compose.yaml` paths are stale and must not be treated as the active deployment layout.

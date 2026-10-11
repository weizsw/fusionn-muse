# Durable job processing

Status: Accepted

## Goals

- Persist every well-formed Submission before slow media preparation and expose preparation failures before a Job exists.
- Deduplicate every resolved Media item before Job work is queued.
- Retry Translation without repeating a completed Transcription.
- Persist source-language Transcriptions so an operator can edit and Retranslate them.
- Preserve queued work and complete Job history across service restarts.

## Identity and admission

A readable Media item is identified primarily by a strong sampled-content signature derived from its size plus the first and last 1 MiB. The filename returned by `mediaintake.CleanVideoFilename`, including the cleaned extension and ignoring its source directory, is stored as a fallback source key and alias.

Content signatures make copies and renamed files converge on one Job while allowing distinct files with the same cleaned filename to remain separate. If the file cannot be read, the normalized filename is used conservatively as the identity. Existing filename-only records migrate into fallback aliases. Database uniqueness constraints, not in-memory checks, arbitrate concurrent submissions.

The torrent webhook first persists immutable Submission input and a pending Preparation run, then returns HTTP 202 with its Submission ID. It performs no media filesystem operations in the request. A background preparation claim resolves media, performs probing/extraction/assembly within a three-hour deadline, and atomically links the Submission to the new or existing Job. Each Preparation run retains its non-secret configuration snapshot and outcome. The input is not edited; wrong parameters require a new Submission.

A Duplicate submission resolves to the existing Job ID and does not change queue position or create an Attempt. This remains true for completed, failed, interrupted, queued, and running Jobs. Each delivered webhook may have a separate Submission receipt; Media item deduplication happens when its identity becomes known. Reprocessing requires an explicit Job action.

New Job admission from preparation uses the same reservation/staging rules. A new Job and its initial processing Attempt are reserved before filesystem staging; for preparation, this reservation and the Submission link commit in one transaction. Staging uses no-replace hardlink/copy with signature validation, and the Attempt becomes claimable only after its checkpoint is stored. Admission failure remains discoverable on the linked Job. Preparation failure before linking remains on the Submission. A separate prepared source cache is released only after canonical no-replace staging and its prepared checkpoint commit. Failed or interrupted admission retains the cache for recovery. Caches used directly as Job staging artifacts remain owned by the Job; unrelated destinations and submitted downloads are never overwritten or cleaned up.

Untracked files use the same webhook/Submission path as downloaded media. Recovery checks recorded/known media paths; removed folder-retry handlers no longer scan for arbitrarily renamed files.

## Durable records

Use a local SQLite database at `/data/automation/meta/fusionn-muse.db` through the pure-Go `modernc.org/sqlite` driver. The service remains a `CGO_ENABLED=0` static build. Database open, initialization, or migration failure prevents startup; there is no non-durable fallback.

SQLite stores:

- immutable Submissions and every pending, preparing, resolved, failed, ignored or interrupted Preparation run;
- one Job per content signature, with normalized filename fallback keys;
- every Attempt and its parent Job ID;
- queued, running, terminal, and interrupted state;
- Attempt start and end timestamps, outcome, failure stage, and error text;
- durable Stage checkpoints, known artifact paths and Job-owned Artifact claims; and
- each Attempt's non-secret settings snapshot.

Settings snapshots include the provider, model, prompts, and relevant processing settings. They never contain API keys or other credentials. Raw application logs remain in files and are not copied into SQLite.

Queued Attempt rows are the authoritative Queue; pending Preparation runs likewise remain authoritative in SQLite before a Job exists. Workers claim work directly from SQLite; an in-memory signal may wake workers but is never queue state. Submission, Preparation run, Job and Attempt history has no automatic retention limit. Preparation has one FIFO worker outside the heavy lanes, with run-isolated caches so duplicate preparation cannot overwrite or clean another Job's media.

Preparation results are retained while committing their outcome is retried; a transient SQLite failure must not rerun extraction or abandon a live claim. Job insertion and Submission linking are atomic and outcome writes are idempotent. After an uncertain commit or interrupted admission, preserve prepared media for recovery rather than deleting potentially owned artifacts.

Only one fusionn-muse service instance may run against this database. Cross-process leases and distributed concurrency are out of scope. WAL commits use `synchronous=FULL`; completed file publication synchronizes its data and containing directory before recording a Stage checkpoint. Storage must actually honor synchronization. Backups remain necessary for disk loss and corruption.

## Attempt and stage behavior

The pipeline records a checkpoint immediately after each stage succeeds. At minimum, recovery distinguishes preparation, move to processing, Transcription persistence, Translation publication, and video delivery. Recovery trusts a checkpoint plus the presence and readability of its required artifact; no content hash is required.

A normal execution keeps the same durable Attempt while it hands work from preparation to Transcription, Translation, and delivery. The handoff persists the next current stage and returns the Attempt to pending state; it does not create another Attempt. A later Attempt is created only for an automatic Translation retry, Manual retry, Resume, or Retranslation.

Stage checkpoint, handoff and terminal-outcome writes retry while the service is running. The completed work stays in memory; a temporary database failure does not repeat ASR/LLM calls or abandon a running Attempt. Queries expose a live `persistence_error` while a commit is blocked. Shutdown cancels this wait; startup then exposes the unfinished Attempt for explicit recovery. Raw errors and checkpoints remain durable whenever storage permits; no system can durably record an error while its only storage is unavailable.

If a filesystem move succeeds but the process stops before its checkpoint commit, Resume reconciles the recorded media path and known `processing`, `failed` and `scraping` locations. Each candidate must match the Media item's identity. Multiple identical copies are acceptable evidence; changed or conflicting copies fail explicitly. Missing files, empty files, directories and symlinks are not valid pipeline artifacts. Recovery never treats a missing persisted Transcription as permission to repeat ASR.

Before writing or reusing canonical media/subtitle paths, reserve their Artifact claims in SQLite. A different Job's claim, an ambiguous legacy owner, or an unowned existing subtitle blocks the operation. Different Media items retain the existing output naming scheme and fail explicitly on a naming conflict, rather than receiving automatic suffixes or overwriting previous results. File movement creates a complete destination without replacement, then removes the source; crashes may leave two copies but must not leave a truncated published destination. Claims establish ownership, not stage completion.

A Job that has Sufficient Chinese subtitle coverage completes without Transcription or Translation. An older sidecar subtitle alone does not qualify and is not reused as the persisted Transcription.

A failure before Transcription persistence fails the Job immediately. A Transcription command that succeeds but cannot persist its artifact also fails the Job immediately. A terminal Transcription failure or exhausted Translation failure moves unfinished media from `processing` to `failed`. An intermediate Translation failure leaves media in `processing` for the automatic retry. A failed Retranslation of an already completed Job leaves its delivered media untouched.

## Persisted Transcriptions

Persist each source-language Transcription as an SRT file in `/data/automation/transcriptions`. Its basename matches the translated subtitle output:

```text
SNOS-123.mp4 -> /data/automation/transcriptions/SNOS-123.srt
```

Write the file through a temporary file and atomic rename. Do not store a second database copy or a content hash. The persisted file is the source of truth, and deliberate operator edits become the input to the next Translation. Each Translation executes against an Attempt-isolated source snapshot; an edit during an already-running Translation requires a later explicit Retranslation.

A Translation retry, Retranslation, or Resume that requires a missing or unreadable Transcription fails explicitly. It never silently retranscribes. Missing artifacts and ownership conflicts require repair and explicit recovery, not repeated automatic Translation retries.

Only a complete successful Translation is atomically written into an Attempt-owned receipt under `transcriptions/.translations/<job>/<attempt>/translated.srt`. Resume can reuse that receipt if its saved source exactly matches the editable Transcription. A source edit invalidates this reuse. These temporary source snapshots are not a second canonical Transcription or a content hash; they establish which input produced a recoverable Translation. A subprocess's partial output is never receipt evidence. Completed Job retranslation preserves the canonical result path until successful completion and uses its own receipt across lane handoffs.

Strong termination before the complete receipt reaches durable storage may require another external request on explicit recovery. External execution is not exactly-once; SQLite and filesystem operations do not form one atomic transaction. Recovery provides discoverable interrupted work, verified reuse where possible and non-overwriting publication, not a promise to eliminate every repeated network call.

A completed Job may be Retranslated when its original video is gone. Media presence is required only when an unfinished Job still needs video delivery.

## Translation retry

A Translation failure creates a new Attempt under the same Job and starts at Translation from the persisted Transcription. It never repeats Transcription.

Allow three automatic retries after the first translation attempt, for at most four translation attempts. Wait 10 seconds between attempts. The retry wait is persisted outside the Translation lane; it does not reserve a worker, and another eligible Translation may run during the delay.

Automatic Translation retries use the original accepted non-secret settings snapshot and the current credential. Successfully cached translation chunks from the failed Attempt may be reused. Every failed Attempt is logged and recorded, but send the error notification only after all retries are exhausted.

The same automatic retry policy applies when a manually requested Retranslation encounters a Translation failure.

## Manual retry, Resume, and Retranslation

A Manual retry applies to the latest failed execution. Resume applies to the latest interrupted execution. Both retain Job identity and start at the first incomplete stage. Retrying/resuming failed or interrupted Retranslation remains Retranslation; a completed Job's result state is not reset. Configuration defaults to the execution being recovered, including a profile previously explicitly selected by the operator; credentials remain current. Explicit current settings are reloaded/validated before Attempt creation and bypass old translation reuse.

A new Retranslation requires a valid persisted Transcription, starts at Translation and bypasses prior translation cache. It uses the latest recorded configuration by default, or explicitly selected current configuration. Publication atomically replaces only the same Job's translated subtitle after success. Failed Retranslation preserves an existing completed Job result; latest execution state is observed separately.

Each Attempt stores its action request and optional previous output name, plus work-path/checkpoint/receipt metadata. Completed-Job work paths stay separate from the canonical result path across stage claims and restart. When publication succeeded but the final outcome did not commit, recovery rechecks the immutable receipt's source before delivery/finalization. A source edit requires a new Translation; an unreadable snapshot blocks recovery rather than silently requesting a model again.

A failed/interrupted Job without a complete Transcription may explicitly select a new safe output basename with the same extension. Reserve every new output path and insert the Attempt/name audit in one transaction; retain Job/Media identity and old ownership claims. The existing media must match its signature. Do not relocate completed Transcriptions or release another Job's claims. This repairs an output-name collision, not missing/corrupt input or ambiguous historical ownership of an existing result.

Matching action/configuration requests reuse an active Attempt; conflicting requests fail rather than silently returning a different operation. Submission action/state validation and run creation share one transaction.

## Restart and shutdown

On startup, pending Preparation runs and Attempts that were queued but never started remain queued and are eligible for automatic claim. In-flight preparation becomes `interrupted` and requires explicit Submission recovery. Attempts that were running, and their unfinished Jobs, become Interrupted and require an explicit Resume.

Failed/ignored or interrupted preparation retains the snapshot of the run being recovered, including previously selected current settings. Current settings must be explicitly selected; an invalid current config creates no new run. Identical concurrent recovery requests reuse the active run. Resolved Submissions reject preparation retry: later operations belong to their Job.

On graceful shutdown, stop claiming work, cancel the running external command, mark its Attempt and unfinished Job Interrupted, and leave queued Attempts queued.

## Failure and recovery checks

| Failure window | Result and recovery |
|---|---|
| ASR or Translation rejection, timeout or subprocess exit | Record the Attempt/stage error; only eligible Translation failures receive bounded automatic retries. Use explicit Job recovery after exhaustion. |
| Checkpoint, handoff or final result temporarily cannot commit | Keep the completed result and retry storage only; expose `persistence_error`. If stopped, expose interruption after restart. |
| Graceful stop or strong process kill | Queued work remains eligible; unfinished running work requires Resume. An unsaved external response may need another request. |
| Complete Translation receipt saved, checkpoint not committed | Resume reuses the receipt only when its saved source still matches the editable Transcription. |
| Move succeeded, path update not committed | Validate Media identity in known locations; accept identical copies, reject changed/conflicting media. |
| SRT absent, empty, unreadable, directory or symlink | Explicit artifact failure; no implicit retranscription or endless automatic model retries. Repair storage before recovery. |
| Different Media item shares a canonical output name, or legacy ownership is ambiguous | Explicit ownership conflict; never overwrite or automatically suffix. Operator must resolve naming/ownership, not just delete a file. |
| Retranslation fails while the Job is already completed | Preserve its canonical subtitle and recorded result path; record the failed Attempt separately. |
| Completed video consumed by downstream processing | Retranslation still works from the persisted Transcription; it does not need or re-deliver that video. |
| Disk loss/corruption, deleted inputs or non-cooperating external writers | No recovery guarantee without repair/backups. Do not infer stage success from an ownership claim or invent lost data. |

Regression checks run against Queue/processor interfaces, real temporary SQLite and files, and replace only external ASR/LLM adapters. `internal/queue/recovery_test.go` covers blocked commits, migration ambiguity and invalid paths; `internal/service/processor/recovery_test.go` covers interrupted file/results, source edits, conflicts and completed-Job retranslation. These are fault-injection tests, not a physical power-loss certification.

## Execution lanes

Remove the current hard-coded 100-heavy-job admission limit. SQLite may retain any number of queued Attempts.

Use independent FIFO lanes for Transcription and Translation. At most one Transcription and one Translation run at a time, and those two operations may overlap. A stage-aware claim preserves FIFO order within each lane.

Preparation, file movement, delivery, and pre-detected light work use concurrent orchestration outside both heavy lanes. They do not occupy a Transcription or Translation slot.

## HTTP API

Keep the HTTP API unauthenticated because it is deployed only on a trusted private network.

The torrent webhook acknowledges only committed input, with a Submission ID and detail Location, before media preparation. Its response is not a success claim about the Job. Unavailable storage returns an error, not an acknowledgement. Malformed or oversized requests are rejected before business admission. An unavailable Muse cannot retain undelivered notifications: the qB script makes one bounded attempt, checks HTTP 202, and leaves missed completions to manual resubmission; no Redis or sender-side outbox is added.

The sole observation/action surface is:

- `GET /api/v1/tasks`: lightweight Job/unresolved-Submission projection, newest creation first, default 50 and maximum 200, kind/status filters and filter-bound keyset cursor;
- `GET /api/v1/tasks/stats`: SQL aggregation over the identical projection and filters;
- `GET /api/v1/tasks/:kind/:id`: transaction-consistent metadata and a bounded history page, default 50 and maximum 200; and
- `POST /api/v1/tasks/:kind/:id/actions`: explicit retry, resume or retranslation with optional current settings and output-name repair.

Resolved Submission rows move into linked Job detail, not duplicate list/statistics rows. Original Submission IDs remain queryable and point to their canonical Job. Job detail combines linked preparation and Attempt history, with immutable source input, snapshots and action/name audit. The list does not load snapshots. Cursor traversal observes live state, not a database export snapshot; refresh after status/resolution changes.

List/statistics status represents latest execution: pending, processing, completed, failed, interrupted or ignored. Preparation's preparing phase maps to processing plus stage preparing. `job_status` retains the Job outcome, so completed Jobs' failed/interrupted retranslation is visible without discarding previous success. Failed and interrupted counts stay separate. Independent requests can differ during concurrent updates. Reading unavailable/corrupt storage fails explicitly, never returns false empty/zero/not-found results.

Old queue/jobs/submissions/file views, event history and folder retry routes are removed outright as an operator-approved breaking change; stored data is untouched. HTTP bodies/queries are bounded. Unknown/invalid input is 400, missing task 404, incompatible state/ownership 409, unavailable storage/service 503. See [README](../../README.md#api-endpoints) for examples and exact fields. There is no second task table, Redis queue or distributed lease system.

## Rollout

Submission persistence, Artifact claims and action/receipt metadata migrate the database to schema version 5 while preserving version-2/3/4 Jobs, Attempts and Submissions. Historical recorded paths seed ownership; a path recorded by multiple Jobs becomes ambiguous and blocks reuse rather than choosing an owner. Unowned historical outputs are not silently adopted. Back up the database before upgrading: stop Muse before copying SQLite files, including any WAL, or use a consistent SQLite backup. Version-2/3/4 binaries reject the upgraded database, so rollback requires compatible code or restoring the pre-upgrade backup while stopped; simply switching to the old image is not sufficient. Existing submissions that never reached Muse or failed before historical Job creation cannot be reconstructed from the database and need manual resubmission.

The first SQLite deployment starts with empty Job history and must happen while no work is in flight. Do not infer historical Jobs from existing folders or subtitle outputs.

The active deployment bind-mounts `/Volumes/External:/data`. Add `/data/automation/transcriptions` and `/data/automation/meta`. The repository's current `docker-compose.yaml` paths are stale and must not be treated as the active deployment layout.

## Deprecated pipeline compatibility

MLX Qwen3 ASR with PySubtrans Custom Server is the only executable pipeline. The operator approved removal of VideoCaptioner/Whisper adapters, scripts and image dependencies after checking that the active database contains only MLX Attempts and declining retention of the older legacy database. Existing/backup snapshots remain unchanged and readable, but VideoCaptioner and empty legacy providers now fail explicitly rather than silently becoming MLX. To migrate a legacy task, explicitly select current MLX settings in the unified action API; this retains the Job identity and stage checkpoints. Legacy Whisper and reflect/thread/batch settings remain snapshot data only. Keep an old image if rollback to legacy execution is needed; the image change does not delete mounted model caches or media.

---
status: accepted
---

# Persist editable source Transcriptions

Persist each source-language Transcription as an atomically written SRT in `/data/automation/transcriptions` and make that file, including deliberate operator edits, the source of truth for Translation retries and Retranslations. A file artifact was chosen over a database blob or temporary work file because it survives restarts, can be edited directly, and prevents repeated Transcription; no second database copy or content hash is kept.

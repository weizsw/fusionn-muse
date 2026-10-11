---
status: accepted
---

# Reserve Job-owned artifact paths without changing output names

Media items are identified by content, so two distinct Jobs can share a filename even though video/subtitle pairing still uses that name. Keep the existing output naming scheme and persist Job-owned Artifact claims before reusing or writing canonical paths; a different or ambiguous owner blocks processing instead of replacing an output or adding an automatic suffix. The operator chose explicit conflicts over changing media-library names. Claims survive Attempts but are not proof of stage completion, and unowned historical subtitles are not silently adopted.

This adds schema-version-4 ownership records, not a second canonical Transcription or a content hash. Legacy recorded paths seed claims; conflicting legacy records stay ambiguous. Resolving a naming conflict requires an explicit ownership/naming decision: deleting a file alone does not release another Job's claim. No cross-process filesystem lease or protection against non-cooperating external writers is implied.

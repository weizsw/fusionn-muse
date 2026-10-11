---
status: accepted
---

# Persist Submissions before Media item preparation

Record every well-formed business Submission and its first pending Preparation run in the existing SQLite database before acknowledging the torrent webhook. Resolve the Media item asynchronously: preparation may be slow or fail before a content signature or Job exists. Keep Submission input immutable, record every Preparation run, and atomically link a resolved Submission to the new or existing Job. This preserves one Job per Media item instead of inventing Jobs for unresolved paths.

SQLite remains authoritative for pending preparation and Job work; wake signals are advisory. No Redis, sender-side queue, or automatic notification redelivery is introduced. Unsent notifications require manual resubmission. A running preparation interrupted by shutdown requires explicit recovery; pending preparation continues after restart. Operator recovery retains recorded settings unless current settings are explicitly selected, and wrong input is corrected with a new Submission rather than editing history.

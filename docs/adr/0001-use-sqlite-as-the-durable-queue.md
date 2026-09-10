---
status: accepted
---

# Use SQLite as the durable queue

Persist Jobs, Attempts, Stage checkpoints, and queued work in `/data/automation/meta/fusionn-muse.db`, and let workers claim queued Attempt rows directly instead of treating the in-memory channel as authoritative. SQLite gives concurrent admission one atomic uniqueness boundary and preserves recovery history across restarts without adding an external service; use the pure-Go `modernc.org/sqlite` driver to preserve the static build, fail startup rather than fall back to volatile state, and support one service instance only.

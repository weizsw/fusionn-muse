---
status: accepted
---

# Identify media by content signature with filename fallback

Identify a readable Media item by a strong sampled-content signature derived from its size plus the first and last 1 MiB. Store the normalized filename from `mediaintake.CleanVideoFilename` as a fallback source key and alias. This makes copies and renamed files converge on one Job while allowing distinct files with the same cleaned filename to remain separate. When the file cannot be read, use the normalized filename key conservatively. SQLite uniqueness constraints arbitrate concurrent reservations, and migrations preserve existing filename keys as fallback aliases.

# Change: Fix config hot-reload not affecting new jobs

## Why
Config hot-reload detects file changes and updates the manager's config, but the processor and executors hold **stale copies** of config structs created at startup. Changing `translate.model` in config.yaml has no effect on new jobs.

## What Changes
- Processor stores `*config.Manager` instead of `*config.Config`
- Executors are recreated with fresh config at the start of each job
- Jobs in progress continue with their original config (only new jobs affected)

## Impact
- Affected code: `internal/service/processor/processor.go`
- No API or config schema changes
- Hot-reload will work as expected for whisper/translate settings


# config-hot-reload Specification

## Purpose
TBD - created by archiving change fix-config-hot-reload. Update Purpose after archive.
## Requirements
### Requirement: Fresh Config Per Job
The processor SHALL read fresh configuration from the config manager when starting each new job, ensuring hot-reloaded config values are used.

#### Scenario: Model changed between jobs
- **WHEN** config `translate.model` is changed from `model-A` to `model-B`
- **AND** a new job starts processing
- **THEN** the new job uses `model-B` for translation

#### Scenario: Job in progress unaffected
- **WHEN** config is changed while a job is processing
- **THEN** the in-progress job continues with its original config values


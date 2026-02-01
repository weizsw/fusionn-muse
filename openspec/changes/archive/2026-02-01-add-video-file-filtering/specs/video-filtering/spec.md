# Video Filtering

## ADDED Requirements

### Requirement: Video Code Pattern Matching
The system SHALL only process video files whose filename contains a valid code pattern matching `[A-Z]{2,5}-\d{3,5}` (case-insensitive).

#### Scenario: Filename with valid code
- **WHEN** a video file is named `SONE-269.mp4`
- **THEN** the file is considered valid for code matching

#### Scenario: Filename with prefixed code
- **WHEN** a video file is named `123SONE-269.mp4` or `hhd800.com@SONE-269.mp4`
- **THEN** the file is considered valid (code extracted from anywhere in filename)

#### Scenario: Filename without code pattern
- **WHEN** a video file is named `advertisement.mp4` or `sample.mp4`
- **THEN** the file is rejected (no code pattern found)

### Requirement: Minimum File Size
The system SHALL reject video files with size ≤200MB as likely ads, samples, or bonus content.

#### Scenario: File above minimum size
- **WHEN** a video file is 500MB
- **THEN** the file passes size validation

#### Scenario: File below minimum size
- **WHEN** a video file is 50MB
- **THEN** the file is rejected regardless of name pattern

### Requirement: Single Video Selection
When processing a folder containing multiple video files, the system SHALL select exactly one video file by applying filters and choosing the largest remaining file.

#### Scenario: Folder with multiple videos, one valid
- **WHEN** a folder contains `SONE-269.mp4` (2GB), `ad1.mp4` (30MB), `sample.mp4` (100MB)
- **THEN** only `SONE-269.mp4` is queued (others fail pattern or size check)

#### Scenario: Folder with multiple valid videos
- **WHEN** a folder contains `SONE-269.mp4` (2GB) and `SONE-269-1.mp4` (1.5GB)
- **THEN** the larger file `SONE-269.mp4` (2GB) is selected

#### Scenario: Folder with no valid videos
- **WHEN** a folder contains only files that fail pattern or size checks
- **THEN** the webhook returns success with "no valid video files found" message

### Requirement: Direct File Path Bypass
When the webhook receives a direct file path (not a folder), the system SHALL process it without filtering, preserving existing behavior.

#### Scenario: Direct video file path
- **WHEN** webhook receives path `/downloads/SONE-269.mp4` (a file, not folder)
- **THEN** the file is queued directly without pattern/size filtering


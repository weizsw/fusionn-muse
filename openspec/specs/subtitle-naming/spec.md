# subtitle-naming Specification

## Purpose
TBD - created by archiving change add-subtitle-language-suffix. Update Purpose after archive.
## Requirements
### Requirement: Configurable Language Suffix

The system SHALL support a configurable language suffix for generated subtitle files.

#### Scenario: Suffix configured as "zh-CN"
- Given: `subtitle.language_suffix` is set to `"zh-CN"`
- When: A video `movie.mp4` is processed
- Then: The output subtitle is named `movie.zh-CN.srt`

#### Scenario: Suffix configured as empty string
- Given: `subtitle.language_suffix` is set to `""` or not configured
- When: A video `movie.mp4` is processed
- Then: The output subtitle is named `movie.srt` (backwards compatible)

#### Scenario: Suffix configured as "chi"
- Given: `subtitle.language_suffix` is set to `"chi"`
- When: A video `movie.mp4` is processed
- Then: The output subtitle is named `movie.chi.srt`

### Requirement: Subtitle Config Structure

The configuration SHALL include a `subtitle` section with a `language_suffix` option.

#### Scenario: Valid YAML configuration
- Given: The config file contains:
  ```yaml
  subtitle:
    language_suffix: "zh-CN"
  ```
- When: The application starts
- Then: The `language_suffix` value is loaded and available

#### Scenario: Missing subtitle section
- Given: The config file does not contain a `subtitle` section
- When: The application starts
- Then: The `language_suffix` defaults to empty string (no suffix)


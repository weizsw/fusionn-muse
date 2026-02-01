# Proposal: Add Configurable Subtitle Language Suffix

## Why

Media servers like Emby, Jellyfin, and Plex use filename suffixes to identify subtitle languages (e.g., `movie.zh-CN.srt` for Simplified Chinese). Currently, Fusionn-Muse generates subtitles with plain names like `abc-123.srt`, which media servers cannot automatically match to languages.

Adding a configurable language suffix enables:
- Automatic language detection by media servers
- Flexibility to use different suffix conventions (e.g., `zh-CN`, `chi`, `chs`, `zh-Hans`)
- Backwards compatibility when suffix is empty

## What Changes

### Config Addition

Add a new `subtitle` section with a `language_suffix` option:

```yaml
subtitle:
  # Language suffix for generated subtitle files
  # Examples: "zh-CN", "chi", "chs", "zh-Hans", "en"
  # Leave empty for no suffix (e.g., "movie.srt")
  # With suffix: "movie.zh-CN.srt"
  language_suffix: "zh-CN"
```

### Filename Format

| Config Value | Input Video | Output Subtitle |
|--------------|-------------|-----------------|
| `""` (empty) | `movie.mp4` | `movie.srt` |
| `"zh-CN"` | `movie.mp4` | `movie.zh-CN.srt` |
| `"chi"` | `movie.mp4` | `movie.chi.srt` |

### Code Changes

1. **config/config.go**: Add `SubtitleConfig` struct with `LanguageSuffix` field
2. **processor/processor.go**: Use suffix when constructing final subtitle filename
3. **config/config.example.yaml**: Document the new option

## Out of Scope

- Changing intermediate subtitle filenames during processing (only final output)
- Auto-detecting language suffix from `translate.target_lang` (may be added later)
- Multiple output formats (SRT only for now)

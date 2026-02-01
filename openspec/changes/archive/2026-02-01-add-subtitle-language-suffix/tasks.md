# Tasks: Add Configurable Subtitle Language Suffix

## Implementation

- [x] **T1** Add `SubtitleConfig` struct to `internal/config/config.go`
  - Add `LanguageSuffix string` field with `mapstructure:"language_suffix"` tag
  - Add `Subtitle SubtitleConfig` to main `Config` struct

- [x] **T2** Update `internal/service/processor/processor.go` to use suffix
  - Modify line 172 to include language suffix when building `cleanSubName`
  - Handle empty suffix (no dot added when suffix is empty)

- [x] **T3** Update `config/config.example.yaml` with new `subtitle` section
  - Add documented example with common suffix values
  - Place after `translate` section (related to subtitle output)

## Validation

- [x] **T4** Test subtitle naming with various suffix values
  - Empty suffix → `movie.srt`
  - `zh-CN` suffix → `movie.zh-CN.srt`
  - Verify media server recognizes language (manual test)

# Change: Improve Chinese subtitle detection in filenames

## Why

Current detection only matches `-C` suffix right before the final extension (`-c\.[^.]+$`). This misses common patterns like:

- `-C` in the middle: `MIDE-939.4k-C.x265.mp4`
- Language codes: `zh`, `chs`, `cht`, `chi`
- Chinese terms: `中文`, `简中`, `繁中`, `软中`, `硬中`

Videos with embedded Chinese subtitles should skip transcription/translation to avoid redundant processing.

## What Changes

- Replace `subtitleSuffixPattern` regex with a more comprehensive detection function
- Match `-C` or `_C` anywhere in filename (case-insensitive), bounded by non-alphanumeric chars
- Match common Chinese subtitle indicators:
  - Language codes: `zh`, `chs`, `cht`, `chi`, `cn`, `gb`, `big5`
  - English abbrevs: `SC` (Simplified Chinese), `TC` (Traditional Chinese)
  - Chinese terms: `中文`, `简中`, `繁中`, `软中`, `硬中`, `字幕`, `内嵌`, `内封`, `中字`, `国语`, `双语`
- Rename `HasSubtitleSuffix` → `HasChineseSubtitle` for clarity

## Impact

- Affected code: `internal/fileops/fileops.go`, `internal/service/processor/processor.go`
- No API or config changes
- More files correctly skip transcription/translation

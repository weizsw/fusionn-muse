# Tasks: improve-chinese-subtitle-detection

## 1. Implementation

- [x] 1.1 Define Chinese subtitle indicator patterns in `fileops.go`:
  - `-C` or `_C` bounded by non-alphanumeric (e.g., `.`, `-`, `_`, start/end)
  - Language codes: `zh`, `chs`, `cht`, `chi`, `cn`, `gb`, `big5` (word-bounded)
  - English abbrevs: `SC`, `TC` (word-bounded, case-insensitive)
  - Chinese terms: `中文`, `简中`, `繁中`, `软中`, `硬中`, `字幕`, `内嵌`, `内封`, `中字`, `国语`, `双语`
- [x] 1.2 Rename `HasSubtitleSuffix` → `HasChineseSubtitle` with improved detection logic
- [x] 1.3 Update `processor.go` to use renamed function
- [x] 1.4 Update comments and log messages

## 2. Verification

- [x] 2.1 Build and verify no compilation errors
- [x] 2.2 Test patterns: `MIDE-939-C.mp4`, `MIDE-939_C.mp4`, `MIDE-939.4k-C.x265.mp4`, `SONE-269.chs.mp4`, `JUR-123.中文.mp4`

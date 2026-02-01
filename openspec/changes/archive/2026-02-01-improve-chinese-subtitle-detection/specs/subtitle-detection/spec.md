# Chinese Subtitle Detection

## MODIFIED Requirements

### Requirement: Detect Chinese Subtitles in Filename
The system SHALL detect Chinese subtitle indicators anywhere in the filename (not just at the end) to skip transcription and translation for files that already have embedded Chinese subtitles.

#### Scenario: -C suffix at end
- **WHEN** filename is `MIDE-939-C.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: -C in middle of filename
- **WHEN** filename is `MIDE-939.4k-C.x265.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: Language code zh
- **WHEN** filename is `SONE-269.zh.mp4` or `SONE-269-zh.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: Language code chs (simplified)
- **WHEN** filename is `JUR-456.chs.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: Language code cht (traditional)
- **WHEN** filename is `JUR-456.cht.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: Chinese term 中文
- **WHEN** filename is `SONE-269.中文.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: Chinese term 简中 (simplified)
- **WHEN** filename is `MIDE-939.简中.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: Chinese term 软中 (soft subtitle)
- **WHEN** filename is `STARS-123.软中.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: Chinese term 字幕
- **WHEN** filename is `SONE-269.字幕.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: Chinese term 内嵌 (embedded)
- **WHEN** filename is `MIDE-939.内嵌.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: Chinese term 内封 (internal)
- **WHEN** filename is `JUR-456.内封.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: Chinese term 中字
- **WHEN** filename is `SONE-269.中字.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: Chinese term 国语 (Mandarin)
- **WHEN** filename is `MIDE-939.国语.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: Chinese term 双语 (bilingual)
- **WHEN** filename is `JUR-456.双语.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: Language code cn
- **WHEN** filename is `SONE-269.cn.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: English abbrev SC (Simplified Chinese)
- **WHEN** filename is `MIDE-939.SC.mp4` or `MIDE-939-sc.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: English abbrev TC (Traditional Chinese)
- **WHEN** filename is `JUR-456.TC.mp4`
- **THEN** Chinese subtitle is detected

#### Scenario: No Chinese indicator
- **WHEN** filename is `SONE-269.mp4` or `SONE-269.x265.mp4`
- **THEN** Chinese subtitle is NOT detected

#### Scenario: Code pattern not confused with indicator
- **WHEN** filename is `ABC-123.mp4` (valid code, no Chinese indicator)
- **THEN** Chinese subtitle is NOT detected


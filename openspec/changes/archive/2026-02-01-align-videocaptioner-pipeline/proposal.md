# Proposal: Align Fusionn-Muse Pipeline with VideoCaptioner

## Summary

Comprehensive analysis revealed several differences between Fusionn-Muse and VideoCaptioner's subtitle processing pipelines. While the core LLM-based components (Split, Optimize, Translate) are **identical** (Fusionn-Muse imports VideoCaptioner's modules directly), key differences exist in the **transcription layer** and **pipeline orchestration**.

## Analysis Results

### ✅ Components That Are Identical (No Changes Needed)

| Component | Implementation | Notes |
|-----------|----------------|-------|
| **SubtitleSplitter** | `app.core.split.split` | Direct import, same module |
| **SubtitleOptimizer** | `app.core.optimize.optimize` | Direct import, same module |
| **LLMTranslator** | `app.core.translate.llm_translator` | Direct import, same module |
| **ASRData** | `app.core.asr.asr_data` | Direct import, same module |

### ⚠️ Components With Differences

#### 1. Transcription (Whisper)

| Aspect | Fusionn-Muse | VideoCaptioner | Impact |
|--------|--------------|----------------|--------|
| **Tool** | Python `faster-whisper` library | `faster-whisper-xxl` CLI binary | Different binaries |
| **Hallucination Filter** | ✅ Fixed (2 keywords) | 2 keywords | Now aligned |
| **VAD Default** | ✅ Fixed (0.4) | 0.4 | Now aligned |
| **VAD Custom Params** | ✅ Removed | None | Now aligned |
| **CLI Features** | N/A (library) | `--sentence`, `--one_word`, `--ff_mdx_kim2` | Not available |
| **Timing Optimization** | Custom in Python | Uses ASRData.optimize_timing() | Different implementation |

**Key CLI flags missing in library mode:**
- `--sentence` / `--one_word` - Built-in sentence/word level segmentation
- `--ff_mdx_kim2` - Voice separation (removes background music)
- `--vad_method` - Alternative VAD methods

#### 2. Pipeline Orchestration

| Step | Fusionn-Muse | VideoCaptioner |
|------|--------------|----------------|
| 1. Transcribe | `transcribe.py` → SRT | `FasterWhisperASR._run()` → ASRData |
| 2. Split | `subtitle_processor.py --split` | `SubtitleSplitter` |
| 3. Optimize | `subtitle_processor.py --optimize` | `SubtitleOptimizer` |
| 4. Translate | `translate.py` | `LLMTranslator` |

**Pipeline Ordering Difference:**

```
Fusionn-Muse (split_sentences + optimize_subtitles):
  transcribe.py → SRT
       ↓
  subtitle_processor.py (split → optimize → save)
       ↓
  translate.py

VideoCaptioner (GUI workflow):
  FasterWhisperASR → ASRData (already sentence-level with --sentence)
       ↓
  SubtitleOptimizer (optional)
       ↓
  SubtitleSplitter (optional, if word-level)
       ↓
  LLMTranslator
```

#### 3. Timing Optimization

| Fusionn-Muse | VideoCaptioner |
|--------------|----------------|
| `optimize_timing()` in `transcribe.py` (custom) | `ASRData.optimize_timing()` method |
| Applied to raw transcription | Applied to sentence-level data |

**Both use similar 3/4 point adjustment, but:**
- Fusionn-Muse: `mid_point = prev.end + (seg.start - prev.end) * 0.75`
- VideoCaptioner: `mid_time = (current.end + next.start) // 2 + gap // 4`

The formulas produce slightly different results.

### 🔍 Root Cause Analysis

The main quality difference likely comes from:

1. **faster-whisper library vs faster-whisper-xxl binary** - The CLI binary has additional features:
   - Better sentence segmentation (--sentence flag)
   - Voice separation for noisy audio (--ff_mdx_kim2)

2. **Word-level to sentence-level conversion** - VideoCaptioner's CLI outputs sentence-level directly when `--sentence` is used, avoiding the LLM sentence-splitting step entirely.

3. **VAD parameters** (FIXED) - Previous aggressive settings were cutting speech short.

## Recommendations

### Option A: Use faster-whisper-xxl Binary (Recommended)

Install `faster-whisper-xxl` in Docker image and call it via subprocess (like VideoCaptioner does), gaining:
- Built-in sentence segmentation
- Voice separation
- Better quality defaults

**Impact:** Significant quality improvement, medium implementation effort

### Option B: Continue with Library + Tune Parameters

Keep current Python library approach but:
1. ✅ Already done: Aligned VAD/hallucination settings
2. Add missing transcription params (beam_size, temperature, etc.) - ✅ Done
3. Ensure `optimize_timing()` uses ASRData method instead of custom

**Impact:** Moderate quality improvement, minimal implementation effort

### Option C: Hybrid Approach

Use library for transcription, but:
- Always generate word-level timestamps
- Use LLM sentence splitting
- This matches VideoCaptioner's "word timestamp" workflow

**Impact:** Good quality, matches VC workflow for word-level mode

## Current Status

After recent fixes:
- ✅ VAD threshold aligned (0.4)
- ✅ Hallucination filter aligned (2 keywords)
- ✅ Removed aggressive VAD params
- ✅ Added transcription quality params

## Acceptance Criteria

1. Same input video produces comparable quality subtitles between both tools
2. No missing conversation segments in Fusionn-Muse output
3. Translation quality matches when using same LLM model


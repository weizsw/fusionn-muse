# Design: Pipeline Comparison Analysis

## Architecture Comparison

### Fusionn-Muse Pipeline

```
┌─────────────────────────────────────────────────────────────────┐
│                        Go Orchestration                         │
├─────────────────────────────────────────────────────────────────┤
│  handler.go → processor.go → whisper.go → translator.go        │
└─────────────────────────────────────────────────────────────────┘
                             │
                    Python Scripts
                             │
    ┌────────────────────────┼────────────────────────┐
    │                        │                        │
    ▼                        ▼                        ▼
transcribe.py        subtitle_processor.py      translate.py
    │                        │                        │
    │ faster-whisper         │ VideoCaptioner        │ VideoCaptioner
    │ (Python library)       │ modules:              │ LLMTranslator
    │                        │ - SubtitleSplitter    │
    │                        │ - SubtitleOptimizer   │
    │                        │                       │
    ▼                        ▼                       ▼
  video → SRT          SRT → SRT (processed)    SRT → SRT (translated)
```

### VideoCaptioner Pipeline

```
┌─────────────────────────────────────────────────────────────────┐
│                     Python GUI/Thread                           │
├─────────────────────────────────────────────────────────────────┤
│  subtitle_pipeline_thread.py → transcript_thread.py             │
└─────────────────────────────────────────────────────────────────┘
                             │
                      Core Modules
                             │
    ┌────────────────────────┼────────────────────────┐
    │                        │                        │
    ▼                        ▼                        ▼
FasterWhisperASR     SubtitleOptimizer        LLMTranslator
    │                SubtitleSplitter              │
    │                        │                     │
    │ faster-whisper-xxl     │ LLM API            │ LLM API
    │ (CLI binary)           │                    │
    │                        │                    │
    ▼                        ▼                    ▼
 audio → ASRData    ASRData → ASRData      ASRData → ASRData
                    (sentence-level)       (with translations)
```

## Key Architectural Differences

### 1. Whisper Implementation

| Aspect | Fusionn-Muse | VideoCaptioner |
|--------|--------------|----------------|
| Invocation | Python library (`faster_whisper.WhisperModel`) | Subprocess CLI (`faster-whisper-xxl`) |
| Output | SRT file (parsed later) | ASRData (in-memory) |
| Sentence Mode | N/A | `--sentence` flag |
| Word Mode | `word_timestamps=True` | `--one_word 1` |

### 2. Data Flow

**Fusionn-Muse:**
```
Video → transcribe.py → SRT file
     → subtitle_processor.py → SRT file  
     → translate.py → SRT file
```

**VideoCaptioner:**
```
Audio → FasterWhisperASR → ASRData object
     → SubtitleOptimizer/Splitter → ASRData object
     → LLMTranslator → ASRData object → save to file
```

### 3. Feature Availability

| Feature | Fusionn-Muse | VideoCaptioner |
|---------|--------------|----------------|
| Voice Separation | ❌ | ✅ (`--ff_mdx_kim2`) |
| Built-in Sentence Seg | ❌ | ✅ (`--sentence`) |
| VAD Methods | silero only | silero, silero-v5, pyannote, etc |
| GPU RTX 50 Detection | ❌ | ✅ |

## Code Analysis

### Hallucination Filter Comparison

**Fusionn-Muse (transcribe.py:36-39):**
```python
HALLUCINATION_KEYWORDS = [
    "请不吝点赞 订阅 转发",
    "打赏支持明镜",
]
```

**VideoCaptioner (faster_whisper.py:209-212):**
```python
hallucination_keywords = [
    "请不吝点赞 订阅 转发",
    "打赏支持明镜",
]
```
✅ **Identical**

### Music Tag Filter Comparison

**Fusionn-Muse (transcribe.py:42):**
```python
MUSIC_TAG_PATTERN = re.compile(r"^[\[【（\(♪♫🎵]")
```

**VideoCaptioner (faster_whisper.py:219):**
```python
if text.startswith(("【", "[", "(", "（")):
```
⚠️ **Similar but different** - Fusionn-Muse also filters music symbols

### Timing Optimization Comparison

**Fusionn-Muse (transcribe.py:79-107):**
```python
def optimize_timing(segments, threshold_ms=1000):
    # Adjusts boundary to 3/4 point
    mid_point = prev.end + (seg.start - prev.end) * 0.75
```

**VideoCaptioner (asr_data.py:465-492):**
```python
def optimize_timing(self, threshold_ms=1000):
    # Different formula
    mid_time = (current_seg.end_time + next_seg.start_time) // 2 + time_gap // 4
```
⚠️ **Different formulas** - Both aim for 3/4 point but calculate differently

### VAD Parameters Comparison

**Fusionn-Muse (transcribe.py:227-232):**
```python
transcribe_opts["vad_parameters"] = {
    "threshold": vad_threshold,
    # No min_speech_duration_ms or min_silence_duration_ms
}
```

**VideoCaptioner (faster_whisper.py:149-159):**
```python
cmd.extend([
    "--vad_filter", "true",
    "--vad_threshold", f"{self.vad_threshold:.2f}",
])
```
✅ **Both use only threshold** - Fusionn-Muse fixed to remove aggressive params

## Summary

The split/optimize/translate steps are **identical** since Fusionn-Muse imports VideoCaptioner's modules directly. The main differences are in:

1. **Transcription tool** - Library vs CLI binary
2. **Data format** - SRT files vs ASRData objects
3. **Timing optimization** - Different formulas (minor impact)

The recent fixes (VAD threshold, hallucination filter, removing aggressive params) should bring the transcription quality much closer. If issues persist, the CLI binary option should be considered.


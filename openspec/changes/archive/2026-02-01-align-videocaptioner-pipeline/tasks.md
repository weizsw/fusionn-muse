# Tasks: Align Fusionn-Muse Pipeline with VideoCaptioner

## Completed ✅

1. [x] Aligned VAD threshold default to 0.4 (matches VideoCaptioner)
2. [x] Reduced hallucination filter to 2 keywords (matches VideoCaptioner exactly)
3. [x] Removed aggressive VAD params (min_speech_duration_ms, min_silence_duration_ms)
4. [x] Added transcription quality params (beam_size=5, best_of=5, etc.)

## Deferred Tasks

The following tasks were deferred for future consideration:

- **Phase 1 (Testing)**: Manual testing to compare Fusionn-Muse vs VideoCaptioner output
- **Phase 2 (Timing)**: Replace custom `optimize_timing()` with ASRData method
- **Phase 3 (Optional)**: Consider faster-whisper-xxl integration if quality issues persist

These are tracked as potential future improvements but not blocking the core alignment work.


#!/usr/bin/env python3
"""
Transcribe video/audio to SRT using faster-whisper.
Usage: python transcribe.py <input> <output.srt> [--model MODEL] [--language LANG]
"""

import argparse
import os
import sys

# Disable XET protocol, force standard HTTP downloads with progress
os.environ["HF_HUB_ENABLE_HF_TRANSFER"] = "0"
os.environ["HF_HUB_DISABLE_XET"] = "1"


def format_timestamp(seconds: float) -> str:
    """Convert seconds to SRT timestamp format (HH:MM:SS,mmm)."""
    hours = int(seconds // 3600)
    minutes = int((seconds % 3600) // 60)
    secs = int(seconds % 60)
    millis = int((seconds % 1) * 1000)
    return f"{hours:02d}:{minutes:02d}:{secs:02d},{millis:03d}"


def check_model_exists(model_name: str, download_root: str) -> bool:
    """Check if model is already downloaded."""
    from huggingface_hub import scan_cache_dir

    try:
        cache_info = scan_cache_dir(download_root)
        for repo in cache_info.repos:
            if model_name in repo.repo_id:
                return True
    except Exception:
        pass
    return False


def transcribe(
    input_path: str,
    output_path: str,
    model_name: str = "large-v2",
    language: str = None,
    initial_prompt: str = None,
):
    """Transcribe audio/video file to SRT format."""
    from faster_whisper import WhisperModel

    download_root = "/app/models"

    # Check if model needs downloading
    if not check_model_exists(model_name, download_root):
        print(
            f"📥 Downloading model: {model_name} (this may take a while...)", flush=True
        )
        print("   Model will be cached for future use.", flush=True)

    # Use CPU with int8 quantization for efficiency
    print(f"Loading model: {model_name}", flush=True)
    model = WhisperModel(
        model_name, device="cpu", compute_type="int8", download_root=download_root
    )

    print(f"Transcribing: {os.path.basename(input_path)}", flush=True)

    # VAD parameters optimized for better speech detection (like VideoCaptioner)
    # Note: silero_v4/v5 vad_method requires faster-whisper-xxl binary, not available in Python lib
    vad_params = dict(
        threshold=0.4,  # Lower threshold = more sensitive (default 0.5)
        neg_threshold=0.25,  # Smoother silence detection
        min_silence_duration_ms=300,  # Shorter silence = more segments
        min_speech_duration_ms=100,  # Keep short speech segments
        max_speech_duration_s=30,  # Force split very long segments
        speech_pad_ms=200,  # Padding around speech
    )

    # Transcribe with word timestamps for better sentence grouping
    segments, info = model.transcribe(
        input_path,
        language=language if language and language != "auto" else None,
        beam_size=5,
        best_of=5,  # More candidates for better accuracy
        patience=1.0,  # Beam search patience
        vad_filter=True,
        vad_parameters=vad_params,
        word_timestamps=True,  # Enable word-level timestamps
        initial_prompt=initial_prompt,  # Context hint for better accuracy
        condition_on_previous_text=True,  # Use previous text as context
        no_speech_threshold=0.6,  # Filter low-confidence speech
        compression_ratio_threshold=2.4,  # Filter hallucinations
    )

    detected_lang = info.language
    print(
        f"Detected language: {detected_lang} (probability: {info.language_probability:.2f})",
        flush=True,
    )

    # Collect all segments first (generator exhaustion)
    raw_segments = list(segments)
    print(f"Raw segments from whisper: {len(raw_segments)}", flush=True)

    # Post-process: group words into proper sentences
    processed_segments = group_into_sentences(raw_segments, detected_lang)

    # Write SRT file
    srt_content = []
    for i, (start, end, text) in enumerate(processed_segments, start=1):
        start_time = format_timestamp(start)
        end_time = format_timestamp(end)

        srt_content.append(f"{i}")
        srt_content.append(f"{start_time} --> {end_time}")
        srt_content.append(text)
        srt_content.append("")

        # Progress output
        print(
            f"  [{start_time}] {text[:50]}{'...' if len(text) > 50 else ''}", flush=True
        )

    # Write output
    with open(output_path, "w", encoding="utf-8") as f:
        f.write("\n".join(srt_content))

    subtitle_count = len(srt_content) // 4
    print(
        f"Transcription complete: {subtitle_count} subtitles written to {os.path.basename(output_path)}",
        flush=True,
    )
    return subtitle_count


def group_into_sentences(segments, language: str = "en"):
    """Group word-level segments into proper sentences.

    Similar to VideoCaptioner's --sentence mode with max_line_width.
    Uses word-level timestamps when available for accurate timing.
    """
    # Max characters per line based on language
    max_chars = 30 if language in ["zh", "ja", "ko"] else 80
    is_cjk = language in ["zh", "ja", "ko"]

    # Flatten to word-level if available, otherwise use segment-level
    words = []
    for segment in segments:
        if hasattr(segment, "words") and segment.words:
            for word in segment.words:
                if word.word and word.word.strip():
                    words.append((word.start, word.end, word.word))
        else:
            # Fallback to segment-level
            text = segment.text.strip()
            if text:
                words.append((segment.start, segment.end, text))

    if not words:
        return []

    # Sentence-ending punctuation
    sentence_enders = {".", "!", "?", "。", "！", "？", "…", "~", "～"}
    # Clause separators (optional break points)
    clause_seps = {",", "，", ";", "；", ":", "：", "、"}

    processed = []
    current_words = []
    current_start = None

    for start, end, word in words:
        word = word.strip()
        if not word:
            continue

        if current_start is None:
            current_start = start

        current_words.append(word)
        current_end = end

        # Build current text
        if is_cjk:
            current_text = "".join(current_words)
        else:
            current_text = " ".join(current_words)

        # Check if we should break
        should_break = False

        # Break on sentence-ending punctuation
        if any(current_text.rstrip().endswith(p) for p in sentence_enders):
            should_break = True
        # Break if line is too long
        elif len(current_text) >= max_chars:
            # Try to break at clause separator
            if any(word.rstrip().endswith(p) for p in clause_seps):
                should_break = True
            # Or just break if way too long
            elif len(current_text) >= max_chars * 1.3:
                should_break = True

        if should_break and current_text.strip():
            processed.append((current_start, current_end, current_text.strip()))
            current_words = []
            current_start = None

    # Don't forget the last segment
    if current_words:
        if is_cjk:
            final_text = "".join(current_words)
        else:
            final_text = " ".join(current_words)
        if final_text.strip():
            processed.append((current_start, current_end, final_text.strip()))

    # Optimize timing: reduce gaps between adjacent segments
    optimized = optimize_timing(processed)

    return optimized


def optimize_timing(segments, threshold_ms: float = 1.0):
    """Optimize subtitle timing by adjusting adjacent segment boundaries.

    If gap between adjacent segments is below threshold, adjust the boundary
    to reduce flicker (like VideoCaptioner's optimize_timing).
    """
    if len(segments) < 2:
        return segments

    result = list(segments)
    for i in range(len(result) - 1):
        start1, end1, text1 = result[i]
        start2, end2, text2 = result[i + 1]

        gap = start2 - end1
        if 0 < gap < threshold_ms:
            # Adjust boundary to 3/4 point (reduces flicker)
            mid_time = end1 + (gap * 0.75)
            result[i] = (start1, mid_time, text1)
            result[i + 1] = (mid_time, end2, text2)

    return result


def main():
    parser = argparse.ArgumentParser(
        description="Transcribe video/audio to SRT using faster-whisper"
    )
    parser.add_argument("input", help="Input video/audio file")
    parser.add_argument("output", help="Output SRT file")
    parser.add_argument(
        "--model",
        "-m",
        default="large-v2",
        help="Whisper model (tiny, base, small, medium, large-v2, large-v3)",
    )
    parser.add_argument(
        "--language",
        "-l",
        default=None,
        help="Source language (auto-detect if not specified)",
    )
    parser.add_argument(
        "--prompt",
        "-p",
        default=None,
        help="Initial prompt for context (e.g., topic, proper nouns)",
    )

    args = parser.parse_args()

    if not os.path.exists(args.input):
        print(f"Error: Input file not found: {args.input}", file=sys.stderr)
        sys.exit(1)

    try:
        count = transcribe(
            args.input, args.output, args.model, args.language, args.prompt
        )
        if count == 0:
            print("Error: No subtitles generated", file=sys.stderr)
            sys.exit(1)
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()

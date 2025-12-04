#!/usr/bin/env python3
"""
Transcribe video/audio to SRT using Python faster-whisper library.

Features:
- Auto video-to-audio extraction via ffmpeg
- VAD filtering (silero)
- Word-level timestamps support
- Hallucination filtering (common ASR artifacts)
- Timing optimization (reduces subtitle flicker)
- Works on both x86-64 and ARM64

Usage: python transcribe.py <input> <output.srt> [--model MODEL] [--language LANG]
"""

import argparse
import os
import re
import sys
import tempfile
from pathlib import Path
from typing import List, NamedTuple

from faster_whisper import WhisperModel

# VideoCaptioner's video2audio utility
from app.core.utils.video_utils import video2audio

# Supported audio formats
SUPPORTED_AUDIO_FORMATS = {"flac", "m4a", "mp3", "wav", "ogg"}
# Video formats that need audio extraction
VIDEO_FORMATS = {"mp4", "mkv", "avi", "mov", "webm", "wmv", "flv", "ts", "m2ts"}

# Hallucination keywords to filter (common ASR artifacts)
HALLUCINATION_KEYWORDS = [
    "请不吝点赞",
    "订阅 转发",
    "打赏支持",
    "感谢观看",
    "thanks for watching",
    "subscribe",
    "like and subscribe",
    "don't forget to subscribe",
    "please subscribe",
    "字幕由",
    "字幕制作",
    "subtitles by",
]

# Music/sound effect tag patterns (to filter out)
MUSIC_TAG_PATTERN = re.compile(r"^[\[【（\(♪♫🎵]")


class Segment(NamedTuple):
    """Processed segment with timing."""
    start: float
    end: float
    text: str
    words: list = None


def is_hallucination(text: str) -> bool:
    """Check if text contains hallucination keywords."""
    text_lower = text.lower()
    return any(kw.lower() in text_lower for kw in HALLUCINATION_KEYWORDS)


def is_music_tag(text: str) -> bool:
    """Check if text is a music/sound effect tag."""
    return bool(MUSIC_TAG_PATTERN.match(text.strip()))


def filter_segments(segments: List[Segment]) -> List[Segment]:
    """Filter out hallucinations and music tags."""
    filtered = []
    for seg in segments:
        text = seg.text.strip()
        if not text:
            continue
        if is_hallucination(text):
            continue
        if is_music_tag(text):
            continue
        filtered.append(seg)
    return filtered


def optimize_timing(segments: List[Segment], threshold_ms: int = 1000) -> List[Segment]:
    """Optimize subtitle timing by adjusting adjacent segment boundaries.
    
    If gap between adjacent segments is below threshold, adjust the boundary
    to 3/4 point between them (reduces flicker).
    """
    if len(segments) < 2:
        return segments
    
    optimized = []
    for i, seg in enumerate(segments):
        if i == 0:
            optimized.append(seg)
            continue
        
        prev = optimized[-1]
        gap_ms = (seg.start - prev.end) * 1000
        
        if 0 < gap_ms < threshold_ms:
            # Adjust boundary to 3/4 point
            mid_point = prev.end + (seg.start - prev.end) * 0.75
            # Update previous segment's end time
            optimized[-1] = Segment(prev.start, mid_point, prev.text, prev.words)
            # Current segment keeps its start time
            optimized.append(seg)
        else:
            optimized.append(seg)
    
    return optimized


def format_timestamp(seconds: float) -> str:
    """Convert seconds to SRT timestamp format (HH:MM:SS,mmm)."""
    hours = int(seconds // 3600)
    minutes = int((seconds % 3600) // 60)
    secs = int(seconds % 60)
    millis = int((seconds - int(seconds)) * 1000)
    return f"{hours:02d}:{minutes:02d}:{secs:02d},{millis:03d}"


def write_srt(segments: List[Segment], output_path: str, word_level: bool = False):
    """Write segments to SRT file."""
    with open(output_path, "w", encoding="utf-8") as f:
        idx = 1
        for segment in segments:
            if word_level and segment.words:
                # Word-level timestamps
                for word in segment.words:
                    f.write(f"{idx}\n")
                    f.write(f"{format_timestamp(word.start)} --> {format_timestamp(word.end)}\n")
                    f.write(f"{word.word.strip()}\n\n")
                    idx += 1
            else:
                # Segment-level timestamps
                f.write(f"{idx}\n")
                f.write(f"{format_timestamp(segment.start)} --> {format_timestamp(segment.end)}\n")
                f.write(f"{segment.text.strip()}\n\n")
                idx += 1
    return idx - 1


def transcribe(
    input_path: str,
    output_path: str,
    model_name: str = "large-v2",
    language: str = None,
    prompt: str = None,
    word_timestamps: bool = False,
    device: str = "cuda",
    compute_type: str = "auto",
    vad_filter: bool = True,
    vad_threshold: float = 0.5,
) -> int:
    """Transcribe audio/video file to SRT.

    Args:
        input_path: Path to input video/audio file
        output_path: Path to output SRT file
        model_name: Whisper model (tiny, base, small, medium, large-v2, large-v3)
        language: Source language code (zh, ja, ko, en, etc.) or None for auto
        prompt: Initial prompt for context
        word_timestamps: Output word-level timestamps
        device: "cuda", "cpu", or "auto"
        compute_type: "float16", "int8", "int8_float16", or "auto"
        vad_filter: Enable VAD filtering
        vad_threshold: VAD threshold (0.0-1.0)

    Returns:
        Number of subtitle segments
    """
    print(f"Transcribing: {os.path.basename(input_path)}", flush=True)
    print(f"Model: {model_name}, Language: {language or 'auto'}, Device: {device}", flush=True)

    # Check if we need to extract audio from video
    ext = Path(input_path).suffix.lower().lstrip(".")
    audio_path = input_path
    temp_audio = None

    if ext in VIDEO_FORMATS:
        # Extract audio to temp file
        temp_audio = tempfile.NamedTemporaryFile(suffix=".wav", delete=False)
        temp_audio.close()

        print("Extracting audio from video...", flush=True)
        if not video2audio(input_path, temp_audio.name):
            raise RuntimeError(f"Failed to extract audio from {input_path}")

        audio_path = temp_audio.name
    elif ext not in SUPPORTED_AUDIO_FORMATS:
        raise ValueError(f"Unsupported format: {ext}. Supported: {SUPPORTED_AUDIO_FORMATS | VIDEO_FORMATS}")

    try:
        # Determine compute type based on device
        if compute_type == "auto":
            if device == "cuda":
                compute_type = "float16"
            else:
                compute_type = "int8"

        print(f"Loading model (compute_type={compute_type})...", flush=True)
        
        # Load model
        model = WhisperModel(
            model_name,
            device=device,
            compute_type=compute_type,
            download_root="/app/models",
        )

        print("Transcribing...", flush=True)

        # Build transcribe options
        transcribe_opts = {
            "word_timestamps": word_timestamps,
            "vad_filter": vad_filter,
        }

        if vad_filter:
            transcribe_opts["vad_parameters"] = {
                "threshold": vad_threshold,
                "min_speech_duration_ms": 250,
                "min_silence_duration_ms": 100,
            }

        if language and language != "auto":
            transcribe_opts["language"] = language

        if prompt:
            transcribe_opts["initial_prompt"] = prompt

        # Run transcription
        segments, info = model.transcribe(audio_path, **transcribe_opts)

        # Convert to our Segment format
        processed_segments = []
        for seg in segments:
            words = list(seg.words) if word_timestamps and seg.words else None
            processed_segments.append(Segment(
                start=seg.start,
                end=seg.end,
                text=seg.text,
                words=words,
            ))
        
        print(f"Detected language: {info.language} (prob: {info.language_probability:.2f})", flush=True)
        print(f"Raw segments: {len(processed_segments)}", flush=True)

        # Filter hallucinations and music tags
        processed_segments = filter_segments(processed_segments)
        print(f"After filtering: {len(processed_segments)}", flush=True)

        # Optimize timing (only for segment-level, not word-level)
        if not word_timestamps:
            processed_segments = optimize_timing(processed_segments)

        # Write to SRT
        segment_count = write_srt(processed_segments, output_path, word_level=word_timestamps)

        print(f"Transcription complete: {segment_count} subtitles written to {os.path.basename(output_path)}", flush=True)
        return segment_count

    finally:
        # Clean up temp audio file
        if temp_audio and os.path.exists(temp_audio.name):
            os.unlink(temp_audio.name)


def main():
    parser = argparse.ArgumentParser(
        description="Transcribe video/audio to SRT using faster-whisper"
    )
    parser.add_argument("input", help="Input video/audio file")
    parser.add_argument("output", help="Output SRT file")
    parser.add_argument(
        "--model", "-m", default="large-v2",
        help="Whisper model (tiny, base, small, medium, large-v2, large-v3, large-v3-turbo)",
    )
    parser.add_argument(
        "--language", "-l", default=None,
        help="Source language code (zh, ja, ko, en, etc.). Auto-detect if not specified.",
    )
    parser.add_argument(
        "--prompt", "-p", default=None,
        help="Initial prompt for context (e.g., topic, proper nouns)",
    )
    parser.add_argument(
        "--word-timestamps", "-w", action="store_true",
        help="Output word-level timestamps (for downstream sentence splitting)",
    )
    parser.add_argument(
        "--device", "-d", default="auto",
        choices=["cuda", "cpu", "auto"],
        help="Device for inference (default: auto)",
    )
    parser.add_argument(
        "--compute-type", "-c", default="auto",
        choices=["float16", "float32", "int8", "int8_float16", "auto"],
        help="Compute type (default: auto - float16 for CUDA, int8 for CPU)",
    )
    parser.add_argument(
        "--no-vad", action="store_true",
        help="Disable VAD filtering",
    )
    parser.add_argument(
        "--vad-threshold", type=float, default=0.5,
        help="VAD threshold 0.0-1.0 (default: 0.5)",
    )

    args = parser.parse_args()

    if not os.path.exists(args.input):
        print(f"Error: Input file not found: {args.input}", file=sys.stderr)
        sys.exit(1)

    try:
        count = transcribe(
            input_path=args.input,
            output_path=args.output,
            model_name=args.model,
            language=args.language,
            prompt=args.prompt,
            word_timestamps=args.word_timestamps,
            device=args.device,
            compute_type=args.compute_type,
            vad_filter=not args.no_vad,
            vad_threshold=args.vad_threshold,
        )
        if count == 0:
            print("Warning: No subtitles generated", file=sys.stderr)
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        import traceback
        traceback.print_exc()
        sys.exit(1)


if __name__ == "__main__":
    main()

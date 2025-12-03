#!/usr/bin/env python3
"""
Transcribe video/audio to SRT using faster-whisper.
Usage: python transcribe.py <input> <output.srt> [--model MODEL] [--language LANG]
"""

import argparse
import os
import sys


def format_timestamp(seconds: float) -> str:
    """Convert seconds to SRT timestamp format (HH:MM:SS,mmm)."""
    hours = int(seconds // 3600)
    minutes = int((seconds % 3600) // 60)
    secs = int(seconds % 60)
    millis = int((seconds % 1) * 1000)
    return f"{hours:02d}:{minutes:02d}:{secs:02d},{millis:03d}"


def transcribe(
    input_path: str,
    output_path: str,
    model_name: str = "large-v2",
    language: str = None,
):
    """Transcribe audio/video file to SRT format."""
    from faster_whisper import WhisperModel

    # Use CPU with int8 quantization for efficiency
    print(f"Loading model: {model_name}", flush=True)
    model = WhisperModel(
        model_name, device="cpu", compute_type="int8", download_root="/app/models"
    )

    print(f"Transcribing: {os.path.basename(input_path)}", flush=True)

    # Transcribe with progress
    segments, info = model.transcribe(
        input_path,
        language=language if language and language != "auto" else None,
        beam_size=5,
        vad_filter=True,  # Filter out non-speech
        vad_parameters=dict(min_silence_duration_ms=500),
    )

    detected_lang = info.language
    print(
        f"Detected language: {detected_lang} (probability: {info.language_probability:.2f})",
        flush=True,
    )

    # Write SRT file
    srt_content = []
    for i, segment in enumerate(segments, start=1):
        start_time = format_timestamp(segment.start)
        end_time = format_timestamp(segment.end)
        text = segment.text.strip()

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

    args = parser.parse_args()

    if not os.path.exists(args.input):
        print(f"Error: Input file not found: {args.input}", file=sys.stderr)
        sys.exit(1)

    try:
        count = transcribe(args.input, args.output, args.model, args.language)
        if count == 0:
            print("Error: No subtitles generated", file=sys.stderr)
            sys.exit(1)
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""
Transcribe video/audio to SRT using faster-whisper-xxl binary.
Uses the same approach as VideoCaptioner for better accuracy.

Usage: python transcribe.py <input> <output.srt> [--model MODEL] [--language LANG] [--prompt PROMPT]
"""

import argparse
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path


def find_whisper_binary():
    """Find faster-whisper-xxl binary."""
    # Check PATH first
    binary = shutil.which("faster-whisper-xxl")
    if binary:
        return binary

    # Check common locations
    locations = [
        "/app/faster-whisper-xxl/faster-whisper-xxl",
        "/usr/local/bin/faster-whisper-xxl",
        "./faster-whisper-xxl",
    ]
    for loc in locations:
        if os.path.isfile(loc) and os.access(loc, os.X_OK):
            return loc

    return None


def transcribe(
    input_path: str,
    output_path: str,
    model_name: str = "large-v2",
    language: str = None,
    prompt: str = None,
):
    """Transcribe audio/video file to SRT format using faster-whisper-xxl binary."""
    binary = find_whisper_binary()
    if not binary:
        raise RuntimeError(
            "faster-whisper-xxl binary not found. Please install it or add it to PATH."
        )

    print(f"Using binary: {binary}", flush=True)
    print(f"Transcribing: {os.path.basename(input_path)}", flush=True)

    # Determine max line width based on language
    is_cjk = language in ["zh", "ja", "ko"]
    max_line_width = 30 if is_cjk else 80

    # Build command with VideoCaptioner-like parameters
    cmd = [
        binary,
        "-m",
        model_name,
        "--model_dir",
        "/app/models",
        input_path,
        "-o",
        str(Path(output_path).parent),  # Output directory
        "--output_format",
        "srt",
        "--print_progress",
        "--beep_off",
        # VAD settings (like VideoCaptioner)
        "--vad_filter",
        "true",
        "--vad_threshold",
        "0.40",
        "--vad_method",
        "silero_v4",  # Same VAD method as VideoCaptioner
        # Word-level timestamps for better sentence splitting
        "--one_word",
        "1",
        # Sentence mode (key feature from VideoCaptioner)
        "--sentence",
        "--max_line_width",
        str(max_line_width),
        "--max_line_count",
        "1",
        "--max_comma",
        "20",
        "--max_comma_cent",
        "50",
    ]

    # Add language if specified
    if language and language != "auto":
        cmd.extend(["-l", language])

    # Add initial prompt if specified
    if prompt:
        cmd.extend(["--initial_prompt", prompt])

    print(f"Command: {' '.join(cmd)}", flush=True)

    # Run transcription
    process = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        encoding="utf-8",
        errors="ignore",
    )

    # Stream output
    subtitle_count = 0
    for line in process.stdout:
        line = line.strip()
        if line:
            print(f"  {line}", flush=True)

            # Parse progress
            if match := re.search(r"(\d+)%", line):
                progress = int(match.group(1))
                if progress == 100:
                    print("Transcription complete!", flush=True)

            # Check for completion
            if "Subtitles are written to" in line:
                print("SRT file written successfully", flush=True)

    process.wait()

    if process.returncode != 0:
        raise RuntimeError(f"faster-whisper-xxl failed with code {process.returncode}")

    # The binary outputs to the same directory with .srt extension
    # We need to rename/move if the output filename doesn't match
    input_base = Path(input_path).stem
    expected_srt = Path(output_path).parent / f"{input_base}.srt"

    if expected_srt != Path(output_path) and expected_srt.exists():
        shutil.move(str(expected_srt), output_path)

    # Verify output exists
    if not os.path.exists(output_path):
        raise RuntimeError(f"Output file not created: {output_path}")

    # Count subtitles
    with open(output_path, "r", encoding="utf-8") as f:
        content = f.read()
        # Count subtitle blocks (number followed by timestamp)
        subtitle_count = len(re.findall(r"^\d+\s*$", content, re.MULTILINE))

    print(
        f"Transcription complete: {subtitle_count} subtitles written to {os.path.basename(output_path)}",
        flush=True,
    )
    return subtitle_count


def main():
    parser = argparse.ArgumentParser(
        description="Transcribe video/audio to SRT using faster-whisper-xxl"
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
            print("Warning: No subtitles generated", file=sys.stderr)
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()

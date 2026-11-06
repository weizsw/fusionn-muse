"""Audio extraction for engines that cannot decode a media container.

Qwen3-ASR loads audio through librosa/soundfile, which reads a WAV file but not
an MP4 or MKV. The service therefore decodes the video once with ffmpeg into the
16 kHz mono WAV that the upstream loader expects, in a temporary directory that
is removed when the request ends.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import wave
from pathlib import Path

# The sample rate Qwen3-ASR and faster-whisper both resample to.
SAMPLE_RATE = 16000

# How much of ffmpeg's stderr is kept for the failure detail. It is logged as a
# bounded DEBUG excerpt only.
STDERR_TAIL_CHARS = 1500
EXTRACT_TIMEOUT_SEC = 3600


class MediaError(RuntimeError):
    """The input could not be decoded."""


def extract_wav(
    source: Path,
    target: Path,
    *,
    ffmpeg: str = "ffmpeg",
    timeout_sec: int = EXTRACT_TIMEOUT_SEC,
) -> Path:
    """Decode ``source`` to a mono 16 kHz WAV at ``target`` and return it."""
    command = [
        ffmpeg,
        "-nostdin",
        "-hide_banner",
        "-loglevel",
        "error",
        "-y",
        "-i",
        str(source),
        "-vn",
        "-ac",
        "1",
        "-ar",
        str(SAMPLE_RATE),
        "-f",
        "wav",
        str(target),
    ]
    try:
        result = subprocess.run(
            command,
            capture_output=True,
            text=True,
            check=False,
            timeout=timeout_sec,
        )
    except FileNotFoundError as error:
        raise MediaError(f"ffmpeg is not installed or not on PATH: {ffmpeg}") from error
    except subprocess.TimeoutExpired as error:
        raise MediaError(f"ffmpeg did not finish within {timeout_sec}s") from error

    if result.returncode != 0 or not target.is_file():
        tail = (result.stderr or "").strip()[-STDERR_TAIL_CHARS:]
        raise MediaError(f"ffmpeg exited with {result.returncode}: {tail}".strip())
    return target


def wav_duration(path: Path) -> float:
    """Length in seconds of a PCM WAV file."""
    with wave.open(str(path), "rb") as handle:
        frames = handle.getnframes()
        rate = handle.getframerate()
    return frames / float(rate) if rate else 0.0


def ffmpeg_available(ffmpeg: str = "ffmpeg") -> bool:
    """True when the ffmpeg command can be resolved."""
    if os.sep in ffmpeg:
        return Path(ffmpeg).is_file()
    return shutil.which(ffmpeg) is not None

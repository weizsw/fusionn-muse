"""Shared doubles for the service's unit tests.

The tests run on a machine without torch, faster-whisper, fastapi or ffmpeg, so
every outside dependency is injected here: a recording logger, an engine that
yields canned segments, and helpers that put stub modules into ``sys.modules``.
"""

from __future__ import annotations

import contextlib
import sys
import types
from pathlib import Path
from typing import Any, Dict, List, Optional, Sequence

from ..engines.base import Engine
from ..srt import Segment


class RecordingLog:
    """A logger that keeps what was logged, in order."""

    def __init__(self) -> None:
        self.records: List[Dict[str, Any]] = []

    def _record(self, level: str, event: str, message: str, **fields) -> None:
        self.records.append(
            {"level": level, "event": event, "msg": message, "fields": fields}
        )

    def info(self, event, message, **fields):
        self._record("info", event, message, **fields)

    def warn(self, event, message, **fields):
        self._record("warn", event, message, **fields)

    def error(self, event, message, **fields):
        self._record("error", event, message, **fields)

    def debug(self, event, message, **fields):
        self._record("debug", event, message, **fields)

    def exception(self, event, message, **fields):
        self._record("exception", event, message, **fields)

    def events(self) -> List[str]:
        return [record["event"] for record in self.records]

    def find(self, event: str) -> List[Dict[str, Any]]:
        return [record for record in self.records if record["event"] == event]

    def one(self, event: str) -> Dict[str, Any]:
        matches = self.find(event)
        if len(matches) != 1:
            raise AssertionError(f"expected exactly one {event!r}, got {len(matches)}")
        return matches[0]


class FakeEngine(Engine):
    """An engine whose model is a list of segments, or an error."""

    name = "fake-engine"

    def __init__(
        self,
        *,
        log: Optional[RecordingLog] = None,
        segments: Sequence[Segment] = (),
        language: str = "fake-language",
        model: str = "fake/model",
        error: Optional[Exception] = None,
        error_after: int = 0,
    ) -> None:
        super().__init__(log or RecordingLog())
        self._segments = list(segments)
        self._language = language
        self._model_name = model
        self._error = error
        # Raise the error while iterating, once this many segments were yielded.
        self._error_after = error_after
        self.load_count = 0
        self.calls: List[Dict[str, Any]] = []

    @property
    def model_name(self) -> str:
        return self._model_name

    def language_for(self, value: str) -> str:
        return self._language if value else ""

    def load(self) -> None:
        self.load_count += 1

    def transcribe(self, media, *, language="", model=None):
        self.calls.append({"media": Path(media), "language": language, "model": model})
        if self._error is not None and self._error_after == 0:
            raise self._error
        for index, segment in enumerate(self._segments):
            if self._error is not None and index == self._error_after:
                raise self._error
            yield segment


def module(name: str, **attributes: Any) -> types.ModuleType:
    """A stub module to stand in for an import the image would provide."""
    stub = types.ModuleType(name)
    for key, value in attributes.items():
        setattr(stub, key, value)
    return stub


@contextlib.contextmanager
def installed(*modules: types.ModuleType):
    """Install stub modules for the duration of the block, then restore."""
    previous = {}
    for stub in modules:
        previous[stub.__name__] = sys.modules.get(stub.__name__)
        sys.modules[stub.__name__] = stub
    try:
        yield
    finally:
        for name, original in previous.items():
            if original is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = original


def write_wav(path: Path, seconds: float = 1.0, rate: int = 16000) -> Path:
    """A real PCM WAV file, so duration and loudness readers have something to read."""
    frames = int(seconds * rate)
    with wave.open(str(path), "wb") as handle:
        handle.setnchannels(1)
        handle.setsampwidth(2)
        handle.setframerate(rate)
        handle.writeframes(b"\x00\x00" * frames)
    return path


def fake_ffmpeg(
    directory: Path,
    *,
    exit_code: int = 0,
    stderr: str = "",
    seconds: float = 1.0,
    rate: int = 16000,
) -> Path:
    """An executable that stands in for ffmpeg.

    Like ffmpeg, it treats the last argument as the output file: it writes a real
    WAV there, or prints to stderr and exits with the given code.
    """
    script = directory / "ffmpeg"
    script.write_text(
        "#!/usr/bin/env python3"
        '\n"""A stand-in for ffmpeg, used by the transcription service tests."""'
        "\nimport sys, wave"
        f"\nif {stderr!r}: print({stderr!r}, file=sys.stderr)"
        f"\nif {int(exit_code)}: sys.exit({int(exit_code)})"
        "\nwith wave.open(sys.argv[-1], 'wb') as handle:"
        "\n    handle.setnchannels(1)"
        "\n    handle.setsampwidth(2)"
        f"\n    handle.setframerate({int(rate)})"
        f"\n    handle.writeframes(b'\\x00\\x00' * int({float(seconds)} * {int(rate)}))"
        "\n",
        encoding="utf-8",
    )
    script.chmod(0o755)
    return script


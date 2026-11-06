"""faster-whisper, the CPU route for Linux machines without an NVIDIA GPU.

The engine reports one segment per decoded window, which is already
sentence-ish, so the cues are written as they come instead of being regrouped.
"""

from __future__ import annotations

import os
from pathlib import Path
from typing import Iterator, Optional

from ..languages import normalize_text, whisper_language
from ..srt import Segment
from .base import Engine, EngineError, EngineUnavailable


class WhisperEngine(Engine):
    name = "faster-whisper"

    def __init__(
        self,
        log,
        *,
        model: str,
        device: str = "cpu",
        compute_type: str = "int8",
        cpu_threads: int = 0,
        beam_size: int = 5,
        vad_filter: bool = True,
        model_dir: str = "",
    ):
        super().__init__(log)
        self._model_name = model
        self._device = device
        self._compute_type = compute_type
        self._cpu_threads = cpu_threads
        self._beam_size = beam_size
        self._vad_filter = vad_filter
        # CTranslate2 keeps its own download directory, separate from the
        # Hugging Face cache the other engine uses.
        self._download_root = os.path.join(model_dir, "whisper") if model_dir else None
        self._model = None

    @property
    def model_name(self) -> str:
        return self._model_name

    def language_for(self, value: str) -> str:
        return whisper_language(value)

    def health_fields(self) -> dict:
        return {
            "model": self._model_name,
            "device": self._device,
            "compute_type": self._compute_type,
        }

    def load(self) -> None:
        try:
            from faster_whisper import WhisperModel
        except ImportError as error:
            raise EngineUnavailable(
                "faster-whisper is not installed in this image; "
                "run the CPU image (asr/Dockerfile.cpu) or set ASR_ENGINE=qwen"
            ) from error
        self._model = WhisperModel(
            self._model_name,
            device=self._device,
            compute_type=self._compute_type,
            cpu_threads=self._cpu_threads,
            download_root=self._download_root,
        )

    def transcribe(
        self,
        media: Path,
        *,
        language: str = "",
        model: Optional[str] = None,
    ) -> Iterator[Segment]:
        if self._model is None:
            raise EngineError("the model was not loaded")
        # faster-whisper decodes the container itself (PyAV ships with it), so no
        # separate audio extraction is needed here.
        segments, info = self._model.transcribe(
            str(media),
            language=language or None,
            beam_size=self._beam_size,
            vad_filter=self._vad_filter,
        )
        # The detected code decides whether words are joined without a space when
        # the request did not name a language.
        effective = language or str(getattr(info, "language", "") or "")
        for segment in segments:
            text = normalize_text(str(segment.text), effective)
            if not text:
                continue
            yield Segment(text=text, start=float(segment.start), end=float(segment.end))

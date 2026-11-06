"""Engine factory for the two Linux deployment routes."""

from __future__ import annotations

from ..settings import Settings
from .base import Engine, EngineError, EngineUnavailable
from .qwen import QwenEngine
from .whisper import WhisperEngine

__all__ = ["Engine", "EngineError", "EngineUnavailable", "create_engine"]


def create_engine(settings: Settings, log) -> Engine:
    """The engine the settings select.

    Importing this module does not import either engine's libraries: torch and
    faster-whisper are imported inside ``load``, so the service starts and
    reports a clear error when the running image does not match ASR_ENGINE.
    """
    if settings.engine == "qwen":
        return QwenEngine(
            log,
            model=settings.model,
            aligner_model=settings.aligner_model,
            device_map=settings.device_map,
            dtype=settings.dtype,
            max_new_tokens=settings.max_new_tokens,
            max_batch_size=settings.max_batch_size,
            model_dir=settings.model_dir,
            ffmpeg=settings.ffmpeg,
        )
    if settings.engine == "whisper":
        return WhisperEngine(
            log,
            model=settings.model,
            device=settings.device,
            compute_type=settings.compute_type,
            cpu_threads=settings.cpu_threads,
            beam_size=settings.beam_size,
            vad_filter=settings.vad_filter,
            model_dir=settings.model_dir,
        )
    raise EngineError(f"unknown engine {settings.engine!r}, expected whisper or qwen")

"""Qwen3-ASR on an NVIDIA GPU, the NVIDIA route.

Upstream does the chunking and the forced alignment internally when
``return_time_stamps`` is on: it splits the audio into 180 s windows, aligns each
window, and merges the offsets. This engine therefore stays a thin adapter that
decodes the video to WAV, asks upstream for the transcript *and* the word-level
timestamps, and hands those to the shared cue builder.
"""

from __future__ import annotations

import os
import tempfile
from pathlib import Path
from typing import Iterator, List, Optional

from ..languages import normalize_text, primary_language, qwen_language
from ..media import extract_wav, ffmpeg_available, wav_duration
from ..srt import Segment, group_segments
from .base import Engine, EngineError, EngineUnavailable


class QwenEngine(Engine):
    name = "qwen3-asr"

    def __init__(
        self,
        log,
        *,
        model: str,
        aligner_model: str = "",
        device_map: str = "cuda:0",
        dtype: str = "bfloat16",
        max_new_tokens: int = 4096,
        max_batch_size: int = 8,
        model_dir: str = "",
        ffmpeg: str = "ffmpeg",
    ):
        super().__init__(log)
        self._model_name = model
        self._aligner_model = aligner_model
        self._device_map = device_map
        self._dtype = dtype
        self._max_new_tokens = max_new_tokens
        self._max_batch_size = max_batch_size
        self._model_dir = model_dir
        self._ffmpeg = ffmpeg
        self._asr = None

    @property
    def model_name(self) -> str:
        return self._model_name

    def language_for(self, value: str) -> str:
        return qwen_language(value)

    def health_fields(self) -> dict:
        return {
            "model": self._model_name,
            "aligner_model": self._aligner_model or "",
            "device_map": self._device_map,
            "dtype": self._dtype,
        }

    def load(self) -> None:
        # huggingface_hub resolves its cache root when it is imported, so the
        # model directory has to be in the environment before the import below.
        if self._model_dir:
            os.environ.setdefault("HF_HOME", self._model_dir)
        try:
            import torch
            from qwen_asr import Qwen3ASRModel
        except ImportError as error:
            raise EngineUnavailable(
                "qwen-asr is not installed in this image; "
                "run the NVIDIA image (asr/Dockerfile.cuda) or set ASR_ENGINE=whisper"
            ) from error
        if not ffmpeg_available(self._ffmpeg):
            raise EngineUnavailable(f"ffmpeg is not available: {self._ffmpeg}")

        torch_dtype = getattr(torch, self._dtype)
        aligner_kwargs = (
            {"device_map": self._device_map, "dtype": torch_dtype}
            if self._aligner_model
            else None
        )
        self._asr = Qwen3ASRModel.from_pretrained(
            self._model_name,
            forced_aligner=self._aligner_model or None,
            forced_aligner_kwargs=aligner_kwargs,
            max_inference_batch_size=self._max_batch_size,
            max_new_tokens=self._max_new_tokens,
            device_map=self._device_map,
            dtype=torch_dtype,
        )
        if not self._aligner_model:
            # A visible degradation, not a failure: without the aligner upstream
            # cannot report timestamps, so every transcript becomes one cue.
            self._log.warn(
                "timestamps_disabled",
                "Forced aligner is disabled, each transcript will be a single cue",
                tool=self.name,
                model=self._model_name,
            )

    def transcribe(
        self,
        media: Path,
        *,
        language: str = "",
        model: Optional[str] = None,
    ) -> Iterator[Segment]:
        if self._asr is None:
            raise EngineError("the model was not loaded")
        cues: List[Segment] = []
        # Upstream loads audio through librosa, which cannot read a video
        # container, so the video is decoded to a 16 kHz mono WAV first. The
        # temporary directory is removed before the cues are handed out.
        with tempfile.TemporaryDirectory(prefix="muse-asr-") as work:
            wav = extract_wav(
                Path(media), Path(work) / f"{Path(media).stem}.wav", ffmpeg=self._ffmpeg
            )
            results = self._asr.transcribe(
                audio=[str(wav)],
                language=[language or None],
                return_time_stamps=bool(self._aligner_model),
            )
            result = results[0] if results else None
            if result is not None:
                transcript = str(getattr(result, "text", "") or "")
                # A mixed clip reports a comma-joined list; the first language
                # decides the joining rule and matches the dominant one.
                detected = (
                    primary_language(str(getattr(result, "language", "") or ""))
                    or language
                )
                items = getattr(getattr(result, "time_stamps", None), "items", None) or []
                if items:
                    timed = [
                        Segment(
                            text=normalize_text(str(getattr(item, "text", "")), detected),
                            start=float(item.start_time),
                            end=float(item.end_time),
                        )
                        for item in items
                    ]
                    cues = group_segments(timed, language=detected, text=transcript)
                else:
                    # No aligner: the whole transcript is the only cue the model
                    # can support.
                    text = normalize_text(transcript, detected)
                    if text:
                        cues = [Segment(text=text, start=0.0, end=wav_duration(wav))]
        for cue in cues:
            yield cue

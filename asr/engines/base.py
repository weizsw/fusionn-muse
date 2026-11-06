"""The engine interface: one transcription back end per deployment route."""

from __future__ import annotations

import abc
from pathlib import Path
from typing import Iterator, Optional

from ..srt import Segment


class EngineError(RuntimeError):
    """Transcription failed; the message is logged as a bounded DEBUG excerpt."""


class EngineUnavailable(EngineError):
    """The engine's dependencies are not present in this image."""


class Engine(abc.ABC):
    """A transcription back end.

    An engine owns its model and nothing else: the service translates paths,
    locks the single request slot, decides the file name and writes the SRT.
    ``load`` runs once at startup so a broken deployment fails before it accepts
    a job, and ``transcribe`` is a generator, which lets the service report
    progress while a long CPU job runs.
    """

    #: Name used in logs and in the health response.
    name = "engine"

    def __init__(self, log):
        self._log = log

    @property
    @abc.abstractmethod
    def model_name(self) -> str:
        """The model actually loaded, not the one the request asked for."""

    @abc.abstractmethod
    def language_for(self, value: str) -> str:
        """This engine's spelling of a configured language, "" for auto-detect."""

    @abc.abstractmethod
    def load(self) -> None:
        """Load the model, raising EngineError when the deployment is broken."""

    @abc.abstractmethod
    def transcribe(
        self,
        media: Path,
        *,
        language: str = "",
        model: Optional[str] = None,
    ) -> Iterator[Segment]:
        """Yield timed segments for one media file.

        ``language`` is already normalized by :meth:`language_for`, and ``model``
        is the value the request carried: the compatible-mode request sends the
        macOS model id, so the engines log it and use their own configured model.
        """

    def health_fields(self) -> dict:
        return {"model": self.model_name}

"""The request contract, independent of the web framework.

The macOS service answers ``POST /transcribe`` with a small JSON body; Linux
keeps that contract exactly — same routes, same status codes (200, 404, 409,
500), same fields — so the Go client and its configuration are unchanged. This
module holds the part that is worth testing without a server: path translation,
the single-request lock, the failure mapping and the log lines.

One deviation, in a case the Go client never produces: a body without a usable
``video_path`` answers 400 with the usual ``{"success": false, "error": ...}``
shape, where the macOS service let FastAPI answer 422 with its own error format.
"""

from __future__ import annotations

import threading
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Tuple

from .engines import Engine, EngineError
from .srt import Segment, write_srt

# One request at a time: transcription saturates the CPU or the GPU, so a second
# job waits for a 409 rather than competing with the first.
BUSY_ERROR = "ASR busy"

DEFAULT_CONTAINER_PREFIX = "/data"

# One progress line per this many segments, so a long CPU job is visibly alive
# without filling the log.
PROGRESS_EVERY_SEGMENTS = 50

# An engine's own failure text can be long (a decoder's stderr, for instance); the
# response is bounded, the DEBUG excerpt keeps more of it.
MAX_ERROR_CHARS = 500


@dataclass(frozen=True)
class TranscribeOutcome:
    """What the HTTP layer should answer and whether the detail was already logged."""

    status: int
    body: Dict[str, Any]
    reported_error: bool = False


@dataclass(frozen=True)
class TranscribeRequest:
    """The request body, with the same defaults as the macOS service."""

    video_path: str
    container_prefix: str = DEFAULT_CONTAINER_PREFIX
    host_prefix: str = ""
    model: str = ""
    language: str = ""
    job_id: str = ""
    attempt: Optional[int] = None
    attempt_id: Optional[int] = None

    @classmethod
    def from_body(
        cls, body: Any
    ) -> Tuple[Optional["TranscribeRequest"], str]:
        """Build a request, or return (None, reason) for a body that cannot work."""
        if not isinstance(body, Mapping):
            return None, "request body must be a JSON object"
        video_path = str(body.get("video_path") or "").strip()
        if not video_path:
            return None, "video_path is required"
        container_prefix = (
            str(body.get("container_prefix") or "").strip() or DEFAULT_CONTAINER_PREFIX
        )
        return (
            cls(
                video_path=video_path,
                container_prefix=container_prefix,
                host_prefix=str(body.get("host_prefix") or "").strip(),
                model=str(body.get("model") or "").strip(),
                language=str(body.get("language") or "").strip(),
                job_id=str(body.get("job_id") or ""),
                attempt=_integer(body.get("attempt")),
                attempt_id=_integer(body.get("attempt_id")),
            ),
            "",
        )

    def correlation(self) -> Dict[str, Any]:
        """The identifier fields every line of one job carries."""
        fields: Dict[str, Any] = {}
        if self.job_id:
            fields["job_id"] = self.job_id
        if self.attempt_id is not None:
            fields["attempt_id"] = self.attempt_id
        if self.attempt is not None:
            # The durable Attempt id is `attempt_id`; this stays the retry ordinal.
            fields["attempt_number"] = self.attempt
        return fields


def _integer(value: Any) -> Optional[int]:
    if value is None or isinstance(value, bool):
        return None
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


def translate_path(container_path: str, container_prefix: str, host_prefix: str) -> Path:
    """Map a path the Go client sees to the path this container sees.

    On Linux the two prefixes are the same, so this is the identity unless a
    deployment mounts the media somewhere else and says so.
    """
    if not container_prefix or not container_path.startswith(container_prefix):
        return Path(container_path)
    relative = container_path[len(container_prefix) :].lstrip("/")
    return Path(host_prefix) / relative


def to_container_path(host_path: Path, host_prefix: str, container_prefix: str) -> str:
    """The reverse mapping, applied to the subtitle path in the response."""
    text = str(host_path)
    if host_prefix and text.startswith(host_prefix):
        return text.replace(host_prefix, container_prefix, 1)
    return text


class TranscriptionService:
    """One transcription at a time, logged end to end."""

    def __init__(
        self,
        *,
        engine: Engine,
        log,
        host_media_path: str = "",
        version: str = "",
    ):
        self._engine = engine
        self._log = log
        self._host_media_path = host_media_path
        self._version = version
        self._lock = threading.Lock()

    @property
    def busy(self) -> bool:
        return self._lock.locked()

    def health(self) -> Dict[str, Any]:
        body: Dict[str, Any] = {
            "status": "healthy",
            "service": "muse-asr",
            "engine": self._engine.name,
            "busy": self.busy,
        }
        body.update(self._engine.health_fields())
        if self._version:
            body["version"] = self._version
        return body

    def transcribe(self, body: Any, *, request_id: str = "") -> TranscribeOutcome:
        payload, problem = TranscribeRequest.from_body(body)
        if payload is None:
            self._log.warn(
                "request_invalid",
                "Request rejected",
                request_id=request_id or None,
                reason=problem,
            )
            return TranscribeOutcome(400, {"success": False, "error": problem})

        fields = payload.correlation()
        if request_id:
            fields["request_id"] = request_id

        if not self._lock.acquire(blocking=False):
            self._log.warn(
                "transcription_busy",
                "ASR is busy with another transcription",
                **fields,
            )
            return TranscribeOutcome(409, {"success": False, "error": BUSY_ERROR})

        try:
            return self._run(payload, fields)
        except Exception as error:  # the outer safety net, as on macOS
            self._log.exception(
                "transcription_failed", "Transcription failed", stage="internal", **fields
            )
            return TranscribeOutcome(
                500,
                {"success": False, "error": f"Transcription failed: {error}"},
                reported_error=True,
            )
        finally:
            self._lock.release()

    def _run(self, payload: TranscribeRequest, fields: Dict[str, Any]) -> TranscribeOutcome:
        # HOST_MEDIA_PATH lets one deployment override the prefix a request
        # carries, which is how a mount that differs from the client's view is
        # corrected without touching config.yaml.
        host_prefix = self._host_media_path or payload.host_prefix
        video = translate_path(payload.video_path, payload.container_prefix, host_prefix)

        if not video.exists():
            self._log.warn(
                "video_missing",
                "Video not found",
                stage="lookup",
                path=str(video),
                **fields,
            )
            return TranscribeOutcome(404, {"success": False, "error": f"Video not found: {video}"})

        language = self._engine.language_for(payload.language)
        if payload.language and not language:
            # Not fatal: auto-detect is a working answer, and the raw value is
            # what an operator needs to fix the configuration.
            self._log.warn(
                "config_invalid",
                "Unknown language, using auto-detect",
                field="language",
                value=payload.language,
                source="request",
                **fields,
            )

        started = time.monotonic()
        started_fields = dict(fields)
        started_fields.update(
            stage="asr",
            tool=self._engine.name,
            model=self._engine.model_name,
            language=language or "auto",
        )
        if payload.model and payload.model != self._engine.model_name:
            # The compatible request carries the macOS model id; naming both makes
            # the substitution visible instead of looking like a misconfiguration.
            started_fields["requested_model"] = payload.model
        self._log.info("transcription_started", "Transcription started", **started_fields)

        segments: List[Segment] = []
        try:
            for segment in self._engine.transcribe(
                video, language=language, model=payload.model or None
            ):
                segments.append(segment)
                if len(segments) % PROGRESS_EVERY_SEGMENTS == 0:
                    self._log.info(
                        "transcription_progress",
                        "Transcription in progress",
                        stage="asr",
                        tool=self._engine.name,
                        segments=len(segments),
                        position_sec=round(float(segment.end), 1),
                        duration_ms=int((time.monotonic() - started) * 1000),
                        **fields,
                    )
        except EngineError as error:
            return self._failed(
                "asr",
                str(error),
                started=started,
                fields=fields,
                tool=self._engine.name,
            )
        except Exception as error:
            self._log.exception(
                "transcription_failed",
                "Transcription failed",
                stage="asr",
                tool=self._engine.name,
                duration_ms=int((time.monotonic() - started) * 1000),
                **fields,
            )
            return TranscribeOutcome(
                500,
                {"success": False, "error": f"Transcription failed: {error}"},
                reported_error=True,
            )

        target = video.with_suffix(".srt")
        try:
            output = write_srt(target, segments)
        except OSError as error:
            self._log.error(
                "transcription_failed",
                "Could not write the subtitle file",
                stage="publish",
                tool=self._engine.name,
                path=str(target),
                errno=error.errno,
                duration_ms=int((time.monotonic() - started) * 1000),
                **fields,
            )
            return TranscribeOutcome(
                500,
                {"success": False, "error": f"Could not write the subtitle file: {target}"},
                reported_error=True,
            )
        if output is None:
            # The engine produced no speech to time. Nothing downstream can use
            # an empty file, so this is a failure like macOS's "no SRT produced".
            self._log.error(
                "transcription_failed",
                "Transcription produced no subtitle",
                stage="publish",
                tool=self._engine.name,
                output_dir=str(target.parent),
                duration_ms=int((time.monotonic() - started) * 1000),
                **fields,
            )
            return TranscribeOutcome(
                500,
                {
                    "success": False,
                    "error": f"{self._engine.name} did not produce any subtitle for {video}",
                },
                reported_error=True,
            )

        container_output = to_container_path(output, host_prefix, payload.container_prefix)
        self._log.info(
            "transcription_completed",
            "Transcription completed",
            stage="asr",
            tool=self._engine.name,
            duration_ms=int((time.monotonic() - started) * 1000),
            segments=len(segments),
            output=container_output,
            **fields,
        )
        return TranscribeOutcome(200, {"success": True, "output_path": container_output})

    def _failed(
        self,
        stage: str,
        detail: str,
        *,
        started: float,
        fields: Dict[str, Any],
        tool: str,
    ) -> TranscribeOutcome:
        """One ERROR with the fixed reason, one DEBUG with the engine's own text."""
        self._log.error(
            "transcription_failed",
            "Transcription failed",
            stage=stage,
            tool=tool,
            duration_ms=int((time.monotonic() - started) * 1000),
            **fields,
        )
        self._log.debug(
            "transcription_failed_detail",
            "Transcription failure detail",
            stage=stage,
            tool=tool,
            preview=detail,
            **fields,
        )        return TranscribeOutcome(
            500,
            {"success": False, "error": f"{tool} failed: {detail[:MAX_ERROR_CHARS]}"},
            reported_error=True,
        )

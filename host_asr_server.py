#!/usr/bin/env python3
"""Host ASR service for mlx-qwen3-asr."""

import os
import subprocess
import threading
import time
from pathlib import Path
from typing import Optional

# Configure logging before third-party imports so a library cannot install its own
# handler first.
from scripts.logfmt import configure, request_id, request_summary

SERVICE = "host-asr"
log = configure(service=SERVICE, component=SERVICE)

import uvicorn  # noqa: E402
from fastapi import FastAPI, JSONResponse, Request, Response  # noqa: E402
from pydantic import BaseModel  # noqa: E402


app = FastAPI(title="MLX Qwen3 ASR Service", version="1.0.0")
_asr_lock = threading.Lock()

# Known routes only, so a caller cannot inject an arbitrary path into a log line.
_ROUTES = {"/health": "/health", "/transcribe": "/transcribe"}


class TranscribeRequest(BaseModel):
    video_path: str
    container_prefix: str = "/data"
    host_prefix: str
    model: Optional[str] = None
    language: Optional[str] = None
    job_id: Optional[str] = None
    attempt: Optional[int] = None
    # The durable Attempt id; `attempt` keeps its retry-ordinal meaning.
    attempt_id: Optional[int] = None


class TranscribeResponse(BaseModel):
    success: bool
    output_path: Optional[str] = None
    error: Optional[str] = None


def request_fields(request) -> dict:
    """The middleware's request ID, when a handler was called through HTTP."""
    incoming = getattr(getattr(request, "state", None), "request_id", "")
    return {"request_id": incoming} if incoming else {}


def mark_error_reported(request) -> None:
    """Record that the handler already logged the failure's detail, so the
    middleware's summary adds the status and duration only."""
    state = getattr(request, "state", None)
    if state is not None:
        state.error_reported = True


def correlation(payload: TranscribeRequest) -> dict:
    fields = {}
    if payload.job_id:
        fields["job_id"] = payload.job_id
    if payload.attempt_id is not None:
        fields["attempt_id"] = payload.attempt_id
    if payload.attempt is not None:
        fields["attempt_number"] = payload.attempt
    return fields


def translate_path(container_path: str, container_prefix: str, host_prefix: str) -> str:
    if not container_path.startswith(container_prefix):
        return container_path

    relative = container_path[len(container_prefix) :].lstrip("/")
    return str(Path(host_prefix) / relative)


def to_container_path(host_path: Path, host_prefix: str, container_prefix: str) -> str:
    host_str = str(host_path)
    if host_prefix and host_str.startswith(host_prefix):
        return host_str.replace(host_prefix, container_prefix, 1)
    return host_str


@app.middleware("http")
async def request_middleware(request: Request, call_next):
    """One request_completed summary per request; handlers read the ID from
    request.state and the response echoes it."""
    incoming = request_id(request.headers.get("x-request-id"))
    request.state.request_id = incoming
    started = time.perf_counter()
    try:
        response = await call_next(request)
    except Exception:
        # Exactly one ERROR for the failure: answering the request here, instead
        # of letting the exception escape, keeps uvicorn from reporting the same
        # traceback a second time.
        log.exception(
            "request_completed",
            "Request failed",
            request_id=incoming,
            method=request.method,
            route=_ROUTES.get(request.url.path, "unmatched"),
            status=500,
            duration_ms=int((time.perf_counter() - started) * 1000),
        )
        response = JSONResponse(status_code=500, content={"detail": "Internal Server Error"})
        response.headers["X-Request-Id"] = incoming
        return response
    duration_ms = int((time.perf_counter() - started) * 1000)
    response.headers["X-Request-Id"] = incoming
    route = _ROUTES.get(request.url.path, "unmatched")

    if response.status_code >= 500 and getattr(request.state, "error_reported", False):
        # The handler already recorded the failure's detail: this line adds the
        # status and duration only, so a failure is one ERROR in total.
        log.warn(
            "request_completed",
            "Request failed",
            request_id=incoming,
            method=request.method,
            route=route,
            status=response.status_code,
            duration_ms=duration_ms,
        )
        return response

    request_summary(
        log,
        route=route,
        method=request.method,
        status=response.status_code,
        incoming=incoming,
        duration_ms=duration_ms,
    )
    return response


@app.get("/health")
async def health():
    return {"status": "healthy", "service": "mlx-qwen3-asr"}


@app.post("/transcribe", response_model=TranscribeResponse)
async def transcribe_video(payload: TranscribeRequest, response: Response, request: Request = None):
    fields = correlation(payload)
    fields.update(request_fields(request))
    if not _asr_lock.acquire(blocking=False):
        response.status_code = 409
        log.warn("transcription_busy", "ASR is busy with another transcription", **fields)
        return TranscribeResponse(success=False, error="ASR busy")

    host_prefix = os.getenv("HOST_MEDIA_PATH", payload.host_prefix)
    video_host = Path(
        translate_path(payload.video_path, payload.container_prefix, host_prefix)
    )

    try:
        if not video_host.exists():
            response.status_code = 404
            error = f"Video not found: {video_host}"
            log.warn("video_missing", "Video not found", stage="lookup", path=str(video_host), **fields)
            return TranscribeResponse(success=False, error=error)

        output_dir = video_host.parent
        started_at = time.time()
        cmd = [
            "mlx-qwen3-asr",
            str(video_host),
            "-f",
            "srt",
            "-o",
            str(output_dir),
            "--quiet",
        ]
        if payload.model:
            cmd.extend(["--model", payload.model])
        if payload.language:
            cmd.extend(["--language", payload.language])

        log.info(
            "transcription_started",
            "Transcription started",
            stage="asr",
            tool="mlx-qwen3-asr",
            model=payload.model,
            language=payload.language,
            **fields,
        )
        log.debug(
            "tool_command",
            "Transcription command",
            tool="mlx-qwen3-asr",
            command=" ".join(cmd),
            **fields,
        )
        result = subprocess.run(cmd, capture_output=True, text=True, check=False)
        duration_ms = int((time.time() - started_at) * 1000)
        if result.returncode != 0:
            response.status_code = 500
            output = "\n".join(
                part for part in (result.stderr.strip(), result.stdout.strip()) if part
            )
            error = f"mlx-qwen3-asr failed ({result.returncode}): {output}"
            # The tool's own output is content we did not author, so the failure
            # is a fixed reason with its structural facts; the text appears once,
            # as a bounded DEBUG excerpt. The response keeps its shape.
            log.error(
                "transcription_failed",
                "Transcription failed",
                stage="asr",
                tool="mlx-qwen3-asr",
                exit_code=result.returncode,
                duration_ms=duration_ms,
                **fields,
            )
            log.debug(
                "transcription_failed_detail",
                "Transcription failure detail",
                stage="asr",
                tool="mlx-qwen3-asr",
                preview=output,
                **fields,
            )
            mark_error_reported(request)
            return TranscribeResponse(success=False, error=error)

        candidates = [
            path
            for path in output_dir.glob("*.srt")
            if path.stem.startswith(video_host.stem)
            and path.stat().st_mtime >= started_at - 1
        ]
        if not candidates:
            response.status_code = 500
            output_dir_text = str(output_dir)
            log.error(
                "transcription_failed",
                "Transcription produced no subtitle",
                stage="publish",
                tool="mlx-qwen3-asr",
                duration_ms=duration_ms,
                output_dir=output_dir_text,
                **fields,
            )
            mark_error_reported(request)
            return TranscribeResponse(
                success=False,
                error=f"mlx-qwen3-asr did not create SRT in {output_dir_text}",
            )

        output_host = max(candidates, key=lambda path: path.stat().st_mtime)
        output_container = to_container_path(
            output_host, host_prefix, payload.container_prefix
        )
        log.info(
            "transcription_completed",
            "Transcription completed",
            stage="asr",
            tool="mlx-qwen3-asr",
            duration_ms=duration_ms,
            output=output_container,
            **fields,
        )
        return TranscribeResponse(success=True, output_path=output_container)

    except Exception as e:
        response.status_code = 500
        log.exception("transcription_failed", "Transcription failed", stage="internal", **fields)
        mark_error_reported(request)
        return TranscribeResponse(success=False, error=f"Transcription failed: {e}")

    finally:
        _asr_lock.release()


if __name__ == "__main__":
    host = os.getenv("ASR_HOST", "0.0.0.0")
    port = int(os.getenv("ASR_PORT", "8766"))
    host_media_path = os.getenv("HOST_MEDIA_PATH")

    log.info(
        "service_started",
        "MLX Qwen3 ASR service started",
        host=host,
        port=port,
        host_media_path=host_media_path,
    )
    # log_config=None keeps uvicorn from installing its own handlers; access logs
    # are replaced by the request_completed summary above.
    uvicorn.run(app, host=host, port=port, log_config=None, access_log=False)

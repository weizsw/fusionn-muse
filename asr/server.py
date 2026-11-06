#!/usr/bin/env python3
"""HTTP entry point for the Linux transcription service.

The two routes, their status codes and their JSON fields are the macOS
service's, so the Go client and its configuration work unchanged. Everything
else — engines, cue building, the request lock — lives in :mod:`asr.service`,
which knows nothing about FastAPI.

Run it as a module, with the repository root as the working directory:

    python -m asr.server
"""

from __future__ import annotations

import os
import sys
import time
from typing import Optional

# Configure logging before third-party imports so a library cannot install its
# own handler first.
from scripts.logfmt import configure, request_id, request_summary

SERVICE = "muse-asr"
log = configure(service=SERVICE, component=SERVICE)

from fastapi import FastAPI, JSONResponse, Request  # noqa: E402

from .engines import EngineError, create_engine  # noqa: E402
from .service import TranscriptionService  # noqa: E402
from .settings import ConfigError, Settings, load_settings  # noqa: E402

# Known routes only, so a caller cannot inject an arbitrary path into a log line.
_ROUTES = {"/health": "/health", "/transcribe": "/transcribe"}

TITLE = "Fusionn Muse ASR Service"


def mark_error_reported(request) -> None:
    """Record that the handler already logged the failure's detail, so the
    middleware's summary adds the status and duration only."""
    state = getattr(request, "state", None)
    if state is not None:
        state.error_reported = True


def request_fields(request) -> dict:
    """The middleware's request ID, when a handler was called through HTTP."""
    incoming = getattr(getattr(request, "state", None), "request_id", "")
    return {"request_id": incoming} if incoming else {}


def create_app(service: TranscriptionService) -> FastAPI:
    """The two routes and the one-line-per-request logging."""
    app = FastAPI(title=TITLE)

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
            # Exactly one ERROR for the failure: answering the request here,
            # instead of letting the exception escape, keeps uvicorn from
            # reporting the same traceback a second time.
            log.exception(
                "request_completed",
                "Request failed",
                request_id=incoming,
                method=request.method,
                route=_ROUTES.get(request.url.path, "unmatched"),
                status=500,
                duration_ms=int((time.perf_counter() - started) * 1000),
            )
            response = JSONResponse(
                status_code=500, content={"detail": "Internal Server Error"}
            )
            response.headers["X-Request-Id"] = incoming
            return response
        duration_ms = int((time.perf_counter() - started) * 1000)
        response.headers["X-Request-Id"] = incoming
        route = _ROUTES.get(request.url.path, "unmatched")

        if response.status_code >= 500 and getattr(request.state, "error_reported", False):
            # The handler already recorded the failure's detail: this line adds
            # the status and duration only, so a failure is one ERROR in total.
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
        return service.health()

    @app.post("/transcribe")
    async def transcribe_video(request: Request):
        try:
            body = await request.json()
        except Exception:
            # A body that is not JSON at all cannot name a video; the service
            # answers the same way for both cases.
            body = None
        outcome = service.transcribe(body, request_id=request_fields(request).get("request_id", ""))
        if outcome.reported_error:
            mark_error_reported(request)
        return JSONResponse(status_code=outcome.status, content=outcome.body)

    return app


def load_engine(settings: Settings) -> TranscriptionService:
    """Load the model and build the service.

    The model is loaded here rather than on the first request, so a deployment
    whose image does not match ASR_ENGINE fails at startup with one clear line
    instead of failing every job.
    """
    engine = create_engine(settings, log)
    log.info(
        "model_load_started",
        "Loading model",
        tool=engine.name,
        model=engine.model_name,
    )
    started = time.monotonic()
    engine.load()
    log.info(
        "model_load_completed",
        "Model loaded",
        tool=engine.name,
        model=engine.model_name,
        duration_ms=int((time.monotonic() - started) * 1000),
    )
    return TranscriptionService(
        engine=engine,
        log=log,
        host_media_path=settings.host_media_path,
        version=settings.version,
    )


def main(argv: Optional[list] = None) -> int:
    try:
        settings = load_settings(os.environ)
    except ConfigError as error:
        log.error("service_failed", str(error), stage="config")
        return 1
    log.info("config_loaded", "Configuration loaded", **settings.summary())

    try:
        service = load_engine(settings)
    except EngineError as error:
        log.error(
            "service_failed",
            "Could not load the model",
            stage="init",
            tool=settings.engine,
            model=settings.model,
            error_type=type(error).__name__,
        )
        log.debug(
            "service_failed_detail",
            "Model load failure detail",
            stage="init",
            tool=settings.engine,
            preview=str(error),
        )
        return 1

    log.info(
        "service_started",
        "ASR service started",
        host=settings.host,
        port=settings.port,
        version=settings.version,
        engine=settings.engine,
        model=settings.model,
    )
    try:
        # Imported here so the config and model failures above report without the
        # server ever having been imported.
        import uvicorn

        # log_config=None keeps uvicorn from installing its own handlers; access
        # logs are replaced by the request_completed summary above.
        uvicorn.run(
            create_app(service),
            host=settings.host,
            port=settings.port,
            log_config=None,
            access_log=False,
        )
    except Exception as error:
        log.error(
            "service_failed",
            "Service stopped",
            stage="listen",
            error_type=type(error).__name__,
        )
        log.debug(
            "service_failed_detail",
            "Listen failure detail",
            stage="listen",
            preview=str(error),
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""
DuoSubs HTTP Service
Runs on host to leverage Metal GPU acceleration.
Called by fusionn container via HTTP.
"""

import gc
import platform
import os
import resource
import threading
import time
import tracemalloc
import zipfile
from pathlib import Path
from typing import Optional

# Configure logging before third-party imports so a library cannot install its own
# handler first.
from scripts.logfmt import configure, request_id, request_summary

SERVICE = "duosubs"
log = configure(service=SERVICE, component=SERVICE)

import torch  # noqa: E402
import uvicorn  # noqa: E402
from fastapi import FastAPI, JSONResponse, Request  # noqa: E402
from pydantic import BaseModel  # noqa: E402
from sentence_transformers import SentenceTransformer  # noqa: E402

from duosubs import (  # noqa: E402
    MergeArgs,
    load_subtitles,
    load_sentence_transformer_model,
    merge_subtitles as duosubs_merge,
    save_subtitles_in_zip,
)

app = FastAPI(title="DuoSubs Service", version="1.0.0")

# Known routes only, so a caller cannot inject an arbitrary path into a log line.
_ROUTES = {"/health": "/health", "/merge": "/merge"}

_model: Optional[SentenceTransformer] = None

# Diagnostic instrumentation for investigating long-running memory growth.
# Enable with DUOSUBS_MEMDEBUG=1. Safe to leave enabled in production; overhead is
# a few microseconds per request plus one tracemalloc snapshot every N requests.
_MEMDEBUG_ENABLED = os.getenv("DUOSUBS_MEMDEBUG", "0") == "1"
_MEMDEBUG_TOP_EVERY = int(os.getenv("DUOSUBS_MEMDEBUG_TOP_EVERY", "25"))
_request_counter = 0
_counter_lock = threading.Lock()

if _MEMDEBUG_ENABLED:
    tracemalloc.start()


def _memory_snapshot(label: str, req_num: int) -> None:
    """Emit a single log line with RSS, Python-tracked, and MPS memory values.

    Interpretation guide (trend across many requests at the 'after_cleanup' label):
      - rss grows + mps_driver grows + py_current flat => PyTorch MPS allocator retention
      - rss grows + py_current grows                   => Python-side leak (see the memory_top_allocation event)
      - rss grows + py_current flat + mps_* flat       => libc/arena fragmentation
    """
    if not _MEMDEBUG_ENABLED:
        return
    try:
        ru = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
        # macOS reports ru_maxrss in bytes; Linux reports in kilobytes.
        rss_mb = ru / (1024 * 1024) if platform.system() == "Darwin" else ru / 1024

        py_current_mb = py_peak_mb = -1.0
        if tracemalloc.is_tracing():
            cur, peak = tracemalloc.get_traced_memory()
            py_current_mb = cur / (1024 * 1024)
            py_peak_mb = peak / (1024 * 1024)

        mps_alloc_mb = mps_driver_mb = -1.0
        if hasattr(torch, "mps") and platform.system() == "Darwin":
            try:
                mps_alloc_mb = torch.mps.current_allocated_memory() / (1024 * 1024)
                mps_driver_mb = torch.mps.driver_allocated_memory() / (1024 * 1024)
            except Exception:
                pass

        log.debug(
            "memory_snapshot",
            "Memory snapshot",
            label=label,
            request=req_num,
            rss_mb=round(rss_mb, 1),
            py_current_mb=round(py_current_mb, 1),
            py_peak_mb=round(py_peak_mb, 1),
            mps_alloc_mb=round(mps_alloc_mb, 1),
            mps_driver_mb=round(mps_driver_mb, 1),
        )
    except Exception as e:
        # Never let instrumentation break a request.
        log.debug("memory_snapshot_failed", "Memory snapshot failed", error=type(e).__name__)


def _memory_top_dump(req_num: int) -> None:
    """Report the largest tracked allocation site as one bounded DEBUG line."""
    if not _MEMDEBUG_ENABLED or not tracemalloc.is_tracing():
        return
    try:
        stats = tracemalloc.take_snapshot().statistics("filename")
        if not stats:
            return
        top = stats[0]
        log.debug(
            "memory_top_allocation",
            "Largest tracked allocation site",
            request=req_num,
            top_file=top.traceback[0].filename if top.traceback else None,
            top_size_kb=round(top.size / 1024, 1),
        )
    except Exception as e:
        log.debug("memory_top_failed", "Allocation summary failed", error=type(e).__name__)


def _get_model() -> SentenceTransformer:
    """Return the singleton SentenceTransformer, loading it once on first use."""
    global _model
    if _model is None:
        args = MergeArgs()
        _model = load_sentence_transformer_model(args, None)
        log.info("model_load_completed", "Sentence transformer model loaded", model=args.model)
    return _model


def _cleanup_torch_memory() -> None:
    """Best-effort release of GPU/MPS memory after each request."""
    gc.collect()
    if torch.cuda.is_available():
        torch.cuda.empty_cache()
    if hasattr(torch, "mps") and platform.system() == "Darwin":
        try:
            torch.mps.empty_cache()
        except Exception:
            pass


class MergeRequest(BaseModel):
    """Request to merge subtitles"""

    primary_path: str
    secondary_path: str
    output_dir: str
    container_prefix: str = "/data"
    host_prefix: str


class MergeResponse(BaseModel):
    """Response from merge operation"""

    success: bool
    output_path: Optional[str] = None
    error: Optional[str] = None


def translate_path(container_path: str, container_prefix: str, host_prefix: str) -> str:
    """Translate container path to host path"""
    if not container_path.startswith(container_prefix):
        return container_path

    relative = container_path[len(container_prefix) :].lstrip("/")
    return str(Path(host_prefix) / relative)


def request_fields(http_request) -> dict:
    """The middleware's request ID, when a handler was called through HTTP."""
    incoming = getattr(getattr(http_request, "state", None), "request_id", "")
    return {"request_id": incoming} if incoming else {}


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

    request_summary(
        log,
        route=_ROUTES.get(request.url.path, "unmatched"),
        method=request.method,
        status=response.status_code,
        incoming=incoming,
        duration_ms=duration_ms,
    )
    return response


@app.get("/health")
async def health():
    """Health check endpoint"""
    return {"status": "healthy", "service": "duosubs"}


@app.post("/merge", response_model=MergeResponse)
async def merge_subtitles(request: MergeRequest, http_request: Request = None):
    """
    Merge two subtitle files using duosubs Python API.
    Paths are translated from container paths to host paths.
    """
    incoming = request_fields(http_request)
    global _request_counter
    with _counter_lock:
        _request_counter += 1
        req_num = _request_counter

    _memory_snapshot("before", req_num)

    host_prefix = os.getenv("HOST_MEDIA_PATH", request.host_prefix)

    primary_host = translate_path(
        request.primary_path, request.container_prefix, host_prefix
    )
    secondary_host = translate_path(
        request.secondary_path, request.container_prefix, host_prefix
    )
    output_dir_host = translate_path(
        request.output_dir, request.container_prefix, host_prefix
    )

    if not Path(primary_host).exists():
        log.warn("input_missing", "Primary subtitle not found", stage="lookup", path=primary_host, **incoming)
        return MergeResponse(
            success=False, error=f"Primary subtitle not found: {primary_host}"
        )

    if not Path(secondary_host).exists():
        log.warn("input_missing", "Secondary subtitle not found", stage="lookup", path=secondary_host, **incoming)
        return MergeResponse(
            success=False, error=f"Secondary subtitle not found: {secondary_host}"
        )

    Path(output_dir_host).mkdir(parents=True, exist_ok=True)

    try:
        basename = Path(primary_host).stem

        args = MergeArgs(
            primary=Path(primary_host),
            secondary=Path(secondary_host),
            output_dir=Path(output_dir_host),
            output_name=basename,
        )

        merge_started = time.perf_counter()
        log.info(
            "merge_started",
            "Subtitle merge started",
            stage="merge",
            primary=primary_host,
            secondary=secondary_host,
            **incoming,
        )

        model = _get_model()
        primary_data, secondary_data = load_subtitles(args)
        merged = duosubs_merge(args, model, primary_data, secondary_data, [False])
        save_subtitles_in_zip(
            args, merged, primary_data.styles, secondary_data.styles
        )

        zip_path = Path(output_dir_host) / f"{basename}.zip"

        if not zip_path.exists():
            log.error(
                "merge_failed",
                "Subtitle merge produced no archive",
                stage="publish",
                duration_ms=int((time.perf_counter() - merge_started) * 1000),
                path=str(zip_path),
                **incoming,
            )
            return MergeResponse(
                success=False,
                error=f"DuoSubs did not create expected ZIP file: {zip_path}",
            )

        with zipfile.ZipFile(zip_path, "r") as zip_ref:
            zip_ref.extractall(output_dir_host)

        combined_file = Path(output_dir_host) / f"{basename}_combined.ass"

        if not combined_file.exists():
            log.error(
                "merge_failed",
                "Combined subtitle missing after extraction",
                stage="extract",
                duration_ms=int((time.perf_counter() - merge_started) * 1000),
                path=str(combined_file),
                **incoming,
            )
            return MergeResponse(
                success=False,
                error=f"Combined ASS file not found after extraction: {combined_file}",
            )

        zip_path.unlink()

        log.info(
            "merge_completed",
            "Subtitle merge completed",
            stage="merge",
            duration_ms=int((time.perf_counter() - merge_started) * 1000),
            output=str(combined_file),
            **incoming,
        )

        output_container = str(combined_file).replace(
            host_prefix, request.container_prefix, 1
        )

        return MergeResponse(success=True, output_path=output_container)

    except Exception as e:
        log.exception("merge_failed", "Subtitle merge failed", stage="merge", **incoming)
        return MergeResponse(success=False, error=f"Merge failed: {str(e)}")

    finally:
        _cleanup_torch_memory()
        _memory_snapshot("after_cleanup", req_num)
        if _MEMDEBUG_ENABLED and req_num % _MEMDEBUG_TOP_EVERY == 0:
            _memory_top_dump(req_num)


if __name__ == "__main__":
    host = os.getenv("DUOSUBS_HOST", "0.0.0.0")
    port = int(os.getenv("DUOSUBS_PORT", "8765"))

    log.info(
        "service_started",
        "DuoSubs service started",
        host=host,
        port=port,
        host_media_path=os.getenv("HOST_MEDIA_PATH"),
    )

    # Eagerly load the model at startup so first request isn't slow
    _get_model()
    _memory_snapshot("startup_after_model_load", 0)

    # log_config=None keeps uvicorn from installing its own handlers; access logs
    # are replaced by the request_completed summary above.
    uvicorn.run(app, host=host, port=port, log_config=None, access_log=False)

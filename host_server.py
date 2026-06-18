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
import tracemalloc
import zipfile
from pathlib import Path
from typing import Optional

import torch
import uvicorn
from fastapi import FastAPI
from pydantic import BaseModel
from sentence_transformers import SentenceTransformer

from duosubs import (
    MergeArgs,
    load_subtitles,
    load_sentence_transformer_model,
    merge_subtitles as duosubs_merge,
    save_subtitles_in_zip,
)

app = FastAPI(title="DuoSubs Service", version="1.0.0")

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
      - rss grows + py_current grows                   => Python-side leak (see TOP dump)
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

        print(
            f"[MEM {label}] req#{req_num} "
            f"rss={rss_mb:.1f}MB "
            f"py_cur={py_current_mb:.1f}MB py_peak={py_peak_mb:.1f}MB "
            f"mps_alloc={mps_alloc_mb:.1f}MB mps_driver={mps_driver_mb:.1f}MB",
            flush=True,
        )
    except Exception as e:
        # Never let instrumentation break a request.
        print(f"[MEM error] {e}", flush=True)


def _memory_top_dump(req_num: int) -> None:
    """Periodically dump tracemalloc top allocations grouped by file."""
    if not _MEMDEBUG_ENABLED or not tracemalloc.is_tracing():
        return
    try:
        snap = tracemalloc.take_snapshot()
        stats = snap.statistics("filename")[:10]
        print(f"[MEM TOP@req{req_num}] top-10 Python allocations by file:", flush=True)
        for stat in stats:
            print(f"  {stat}", flush=True)
    except Exception as e:
        print(f"[MEM top error] {e}", flush=True)


def _get_model() -> SentenceTransformer:
    """Return the singleton SentenceTransformer, loading it once on first use."""
    global _model
    if _model is None:
        args = MergeArgs()
        _model = load_sentence_transformer_model(args, None)
        print(f"Model loaded: {args.model}", flush=True)
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


@app.get("/health")
async def health():
    """Health check endpoint"""
    return {"status": "healthy", "service": "duosubs"}


@app.post("/merge", response_model=MergeResponse)
async def merge_subtitles(request: MergeRequest):
    """
    Merge two subtitle files using duosubs Python API.
    Paths are translated from container paths to host paths.
    """
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
        return MergeResponse(
            success=False, error=f"Primary subtitle not found: {primary_host}"
        )

    if not Path(secondary_host).exists():
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

        print(f"Merging: {primary_host} + {secondary_host}", flush=True)

        model = _get_model()
        primary_data, secondary_data = load_subtitles(args)
        merged = duosubs_merge(args, model, primary_data, secondary_data, [False])
        save_subtitles_in_zip(
            args, merged, primary_data.styles, secondary_data.styles
        )

        zip_path = Path(output_dir_host) / f"{basename}.zip"

        if not zip_path.exists():
            return MergeResponse(
                success=False,
                error=f"DuoSubs did not create expected ZIP file: {zip_path}",
            )

        with zipfile.ZipFile(zip_path, "r") as zip_ref:
            zip_ref.extractall(output_dir_host)

        combined_file = Path(output_dir_host) / f"{basename}_combined.ass"

        if not combined_file.exists():
            return MergeResponse(
                success=False,
                error=f"Combined ASS file not found after extraction: {combined_file}",
            )

        zip_path.unlink()

        print(f"Merge completed: {combined_file}", flush=True)

        output_container = str(combined_file).replace(
            host_prefix, request.container_prefix, 1
        )

        return MergeResponse(success=True, output_path=output_container)

    except Exception as e:
        return MergeResponse(success=False, error=f"Merge failed: {str(e)}")

    finally:
        _cleanup_torch_memory()
        _memory_snapshot("after_cleanup", req_num)
        if _MEMDEBUG_ENABLED and req_num % _MEMDEBUG_TOP_EVERY == 0:
            _memory_top_dump(req_num)


if __name__ == "__main__":
    host = os.getenv("DUOSUBS_HOST", "0.0.0.0")
    port = int(os.getenv("DUOSUBS_PORT", "8765"))

    print(f"Starting DuoSubs Service on {host}:{port}")
    print(
        f"Host media path: {os.getenv('HOST_MEDIA_PATH', 'not set - pass via request')}"
    )

    # Eagerly load the model at startup so first request isn't slow
    _get_model()
    _memory_snapshot("startup_after_model_load", 0)

    uvicorn.run(app, host=host, port=port)

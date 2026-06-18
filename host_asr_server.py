#!/usr/bin/env python3
"""Host ASR service for mlx-qwen3-asr."""

import os
import subprocess
import threading
import time
from pathlib import Path
from typing import Optional

import uvicorn
from fastapi import FastAPI, Response
from pydantic import BaseModel


app = FastAPI(title="MLX Qwen3 ASR Service", version="1.0.0")
_asr_lock = threading.Lock()


class TranscribeRequest(BaseModel):
    video_path: str
    container_prefix: str = "/data"
    host_prefix: str
    model: Optional[str] = None
    language: Optional[str] = None


class TranscribeResponse(BaseModel):
    success: bool
    output_path: Optional[str] = None
    error: Optional[str] = None


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


@app.get("/health")
async def health():
    return {"status": "healthy", "service": "mlx-qwen3-asr"}


@app.post("/transcribe", response_model=TranscribeResponse)
async def transcribe_video(request: TranscribeRequest, response: Response):
    if not _asr_lock.acquire(blocking=False):
        response.status_code = 409
        return TranscribeResponse(success=False, error="ASR busy")

    host_prefix = os.getenv("HOST_MEDIA_PATH", request.host_prefix)
    video_host = Path(
        translate_path(request.video_path, request.container_prefix, host_prefix)
    )

    try:
        if not video_host.exists():
            response.status_code = 404
            return TranscribeResponse(
                success=False, error=f"Video not found: {video_host}"
            )

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
        if request.model:
            cmd.extend(["--model", request.model])
        if request.language:
            cmd.extend(["--language", request.language])

        print(f"Transcribing: {' '.join(cmd)}", flush=True)
        result = subprocess.run(cmd, capture_output=True, text=True, check=False)
        if result.returncode != 0:
            response.status_code = 500
            return TranscribeResponse(
                success=False,
                error=(
                    f"mlx-qwen3-asr failed ({result.returncode}): "
                    f"{result.stderr.strip() or result.stdout.strip()}"
                ),
            )

        candidates = [
            path
            for path in output_dir.glob("*.srt")
            if path.stem.startswith(video_host.stem)
            and path.stat().st_mtime >= started_at - 1
        ]
        if not candidates:
            response.status_code = 500
            return TranscribeResponse(
                success=False, error=f"mlx-qwen3-asr did not create SRT in {output_dir}"
            )

        output_host = max(candidates, key=lambda path: path.stat().st_mtime)
        output_container = to_container_path(
            output_host, host_prefix, request.container_prefix
        )
        print(f"Transcription completed: {output_host}", flush=True)
        return TranscribeResponse(success=True, output_path=output_container)

    except Exception as e:
        response.status_code = 500
        return TranscribeResponse(success=False, error=f"Transcription failed: {e}")

    finally:
        _asr_lock.release()


if __name__ == "__main__":
    host = os.getenv("ASR_HOST", "0.0.0.0")
    port = int(os.getenv("ASR_PORT", "8766"))

    print(f"Starting MLX Qwen3 ASR Service on {host}:{port}")
    print(
        f"Host media path: {os.getenv('HOST_MEDIA_PATH', 'not set - pass via request')}"
    )
    uvicorn.run(app, host=host, port=port)

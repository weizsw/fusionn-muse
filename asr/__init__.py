"""Linux transcription service for Fusionn-Muse.

The main image runs the queue and the pipeline on macOS and Linux alike; only
transcription is platform specific. On macOS the transcription tool runs on the
host over HTTP (``host_asr_server.py``). On Linux this package is the service
that answers the same protocol inside a container, with one engine per
deployment route:

* ``whisper``: faster-whisper on the CPU, for Linux machines without an NVIDIA
  GPU. Experimental.
* ``qwen``: Qwen3-ASR on an NVIDIA GPU. Experimental.

Both routes keep the ``/transcribe`` request and response shape, so the Go
client needs no change: the service writes the subtitle next to the input video
in the shared data directory and returns that path.
"""

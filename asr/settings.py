"""Runtime settings for the Linux transcription service.

Everything is read from the environment so one image can be pointed at another
model or device without a rebuild. ``load_settings`` takes the mapping to read,
which keeps the tests free of ``os.environ`` patching.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Mapping, Optional

# Deployment routes. Each one is a separately built image; the environment picks
# which engine this process loads.
ENGINES = ("whisper", "qwen")

# The model each engine runs when ASR_MODEL is not set. The request carries the
# macOS tool's model id, which is meaningless on this side: the effective model
# is this value, and the service logs the requested one for diagnosis.
DEFAULT_MODEL = {
    "whisper": "large-v3-turbo",
    "qwen": "Qwen/Qwen3-ASR-1.7B",
}

# Qwen3-ASR timestamps come from a separate forced aligner. An empty
# ASR_ALIGNER_MODEL disables it, which is a documented degradation: the service
# then writes one cue for the whole transcript instead of per-sentence cues.
DEFAULT_ALIGNER_MODEL = "Qwen/Qwen3-ForcedAligner-0.6B"

DEFAULT_HOST = "0.0.0.0"
DEFAULT_PORT = 8766
DEFAULT_MODEL_DIR = "/models"
DEFAULT_FFMPEG = "ffmpeg"

# torch dtype names accepted by ASR_DTYPE.
DTYPES = ("bfloat16", "float16", "float32")


class ConfigError(ValueError):
    """A setting cannot be used; the service reports it and stops."""


def _value(env: Mapping[str, str], name: str, default: str = "") -> str:
    raw = env.get(name)
    if raw is None:
        return default
    return raw.strip()


def _integer(env: Mapping[str, str], name: str, default: int, minimum: int) -> int:
    text = _value(env, name)
    if not text:
        return default
    try:
        value = int(text)
    except ValueError as error:
        raise ConfigError(f"{name} must be an integer, got {text!r}") from error
    if value < minimum:
        raise ConfigError(f"{name} must be at least {minimum}, got {value}")
    return value


def _boolean(env: Mapping[str, str], name: str, default: bool) -> bool:
    text = _value(env, name).lower()
    if not text:
        return default
    if text in ("1", "true", "yes", "on"):
        return True
    if text in ("0", "false", "no", "off"):
        return False
    raise ConfigError(f"{name} must be a boolean, got {text!r}")


@dataclass(frozen=True)
class Settings:
    """The effective configuration of one transcription service process."""

    engine: str
    model: str
    language: str
    host: str
    port: int
    host_media_path: str
    model_dir: str
    version: str
    ffmpeg: str
    # whisper engine
    device: str
    compute_type: str
    cpu_threads: int
    beam_size: int
    vad_filter: bool
    # qwen engine
    aligner_model: str
    device_map: str
    dtype: str
    max_new_tokens: int
    max_batch_size: int

    def summary(self) -> dict:
        """The effective settings as log fields.

        Only the fields the running engine uses are included, so one startup
        line shows every value that can affect the result.
        """
        fields: dict = {
            "engine": self.engine,
            "model": self.model,
            "host": self.host,
            "port": self.port,
            "model_dir": self.model_dir,
            "host_media_path": self.host_media_path,
            "language": self.language or "auto",
        }
        if self.engine == "qwen":
            fields.update(
                {
                    "aligner_model": self.aligner_model or "",
                    "device_map": self.device_map,
                    "dtype": self.dtype,
                    "max_new_tokens": self.max_new_tokens,
                    "max_batch_size": self.max_batch_size,
                }
            )
        else:
            fields.update(
                {
                    "device": self.device,
                    "compute_type": self.compute_type,
                    "cpu_threads": self.cpu_threads,
                    "beam_size": self.beam_size,
                    "vad_filter": self.vad_filter,
                }
            )
        return fields


def load_settings(env: Optional[Mapping[str, str]] = None) -> Settings:
    """Read the settings, raising ConfigError for a value that cannot be used."""
    source: Mapping[str, str] = env if env is not None else {}
    engine = _value(source, "ASR_ENGINE", ENGINES[0]).lower()
    if engine not in ENGINES:
        raise ConfigError(
            f"ASR_ENGINE must be one of {', '.join(ENGINES)}, got {engine!r}"
        )
    dtype = _value(source, "ASR_DTYPE", DTYPES[0]).lower()
    if dtype not in DTYPES:
        raise ConfigError(f"ASR_DTYPE must be one of {', '.join(DTYPES)}, got {dtype!r}")

    return Settings(
        engine=engine,
        model=_value(source, "ASR_MODEL") or DEFAULT_MODEL[engine],
        language=_value(source, "ASR_LANGUAGE"),
        host=_value(source, "ASR_HOST", DEFAULT_HOST),
        port=_integer(source, "ASR_PORT", DEFAULT_PORT, 1),
        host_media_path=_value(source, "HOST_MEDIA_PATH"),
        model_dir=_value(source, "ASR_MODEL_DIR", DEFAULT_MODEL_DIR),
        version=_value(source, "ASR_VERSION", "dev"),
        ffmpeg=_value(source, "ASR_FFMPEG", DEFAULT_FFMPEG),
        device=_value(source, "ASR_DEVICE", "cpu"),
        compute_type=_value(source, "ASR_COMPUTE_TYPE", "int8"),
        cpu_threads=_integer(source, "ASR_CPU_THREADS", 0, 0),
        beam_size=_integer(source, "ASR_BEAM_SIZE", 5, 1),
        vad_filter=_boolean(source, "ASR_VAD_FILTER", True),
        aligner_model=_value(source, "ASR_ALIGNER_MODEL", DEFAULT_ALIGNER_MODEL),
        device_map=_value(source, "ASR_DEVICE_MAP", "cuda:0"),
        dtype=dtype,
        max_new_tokens=_integer(source, "ASR_MAX_NEW_TOKENS", 4096, 1),
        max_batch_size=_integer(source, "ASR_MAX_BATCH_SIZE", 8, 1),
    )

"""Language names shared by the Linux engines.

The request carries whatever the operator configured for the macOS tool, so the
value can be an ISO code (``zh``), a display name (``Chinese``) or a native name
(``中文``). Each engine needs its own spelling: Qwen3-ASR takes the display name
from its supported-language list, faster-whisper takes an ISO code. An unknown
value resolves to "auto" instead of failing the job, and the service logs a
warning so the mistake stays visible.
"""

from __future__ import annotations

import re
from typing import Optional

# The languages Qwen3-ASR accepts, in upstream's wording. Keep this list in step
# with qwen_asr (see SUPPORTED_LANGUAGES in the installed package).
QWEN_LANGUAGES = (
    "Chinese",
    "English",
    "Cantonese",
    "Arabic",
    "German",
    "French",
    "Spanish",
    "Portuguese",
    "Indonesian",
    "Italian",
    "Korean",
    "Russian",
    "Thai",
    "Vietnamese",
    "Japanese",
    "Turkish",
    "Hindi",
    "Malay",
    "Dutch",
    "Swedish",
    "Danish",
    "Finnish",
    "Polish",
    "Czech",
    "Filipino",
    "Persian",
    "Greek",
    "Romanian",
    "Hungarian",
    "Macedonian",
)

# Whisper's language codes, one per language above. faster-whisper accepts these
# codes and its own English names; the codes keep one spelling for both engines.
WHISPER_CODES = {
    "Chinese": "zh",
    "English": "en",
    "Cantonese": "yue",
    "Arabic": "ar",
    "German": "de",
    "French": "fr",
    "Spanish": "es",
    "Portuguese": "pt",
    "Indonesian": "id",
    "Italian": "it",
    "Korean": "ko",
    "Russian": "ru",
    "Thai": "th",
    "Vietnamese": "vi",
    "Japanese": "ja",
    "Turkish": "tr",
    "Hindi": "hi",
    "Malay": "ms",
    "Dutch": "nl",
    "Swedish": "sv",
    "Danish": "da",
    "Finnish": "fi",
    "Polish": "pl",
    "Czech": "cs",
    "Filipino": "tl",
    # Whisper spells Persian "fa" and Greek "el" in ISO 639-1.
    "Persian": "fa",
    "Greek": "el",
    "Romanian": "ro",
    "Hungarian": "hu",
    "Macedonian": "mk",
}

# Languages written without spaces between words, where a joined cue must not
# introduce one (ported from mlx-qwen3-asr's tokenizer).
NO_SPACE_LANGUAGES = frozenset(
    {
        "chinese",
        "zh",
        "zh-cn",
        "zh-tw",
        "zh-hans",
        "zh-hant",
        "cantonese",
        "yue",
        "japanese",
        "ja",
        "jp",
    }
)

# Aliases mapped onto the names above: ISO codes for every supported language,
# plus the native and Chinese spellings operators commonly configure.
_ALIASES = {
    **{name.lower(): name for name in QWEN_LANGUAGES},
    **{code: name for name, code in WHISPER_CODES.items()},
    "mandarin": "Chinese",
    "putonghua": "Chinese",
    "zh-hans": "Chinese",
    "zh-hant": "Chinese",
    "中文": "Chinese",
    "汉语": "Chinese",
    "普通话": "Chinese",
    "简体中文": "Chinese",
    "繁体中文": "Chinese",
    "粤语": "Cantonese",
    "广东话": "Cantonese",
    "日文": "Japanese",
    "日语": "Japanese",
    "日本語": "Japanese",
    "韩语": "Korean",
    "한국어": "Korean",
    "英语": "English",
    "英文": "English",
    "farsi": "Persian",
    "tagalog": "Filipino",
}

# "auto" and friends mean "let the model detect it".
_AUTO_VALUES = frozenset({"", "auto", "automatic", "none", "null", "-"})

_WIDE = re.compile(r"\s+")
_SPACE_BETWEEN_WIDE = re.compile(r"(?<=[^\x00-\x7f])\s+(?=[^\x00-\x7f])")


def primary_language(value: Optional[str]) -> str:
    """The first of a comma-joined language list ("Chinese,English")."""
    return (value or "").split(",")[0].strip()


def canonical_language(value: Optional[str]) -> str:
    """The Qwen display name for a configured value, or "" for auto-detect."""
    text = (value or "").strip()
    if text.lower() in _AUTO_VALUES:
        return ""
    return _ALIASES.get(text.lower(), "")


def qwen_language(value: Optional[str]) -> str:
    """The value upstream's ``language`` argument expects, or "" for auto."""
    return canonical_language(value)


def whisper_language(value: Optional[str]) -> str:
    """The ISO code faster-whisper expects, or "" for auto-detect."""
    return WHISPER_CODES.get(canonical_language(value), "")


def is_no_space_language(value: Optional[str]) -> bool:
    """True for languages whose words are joined without a separator."""
    text = primary_language(value).lower()
    return text in NO_SPACE_LANGUAGES


def normalize_text(text: str, language: Optional[str] = None) -> str:
    """Whitespace-normalized cue text.

    Runs of whitespace collapse to one space, and for a no-space language the
    space between two wide characters is dropped, because the aligner emits one
    segment per character and joining can leave one behind. Spaces around Latin
    words survive.
    """
    normalized = _WIDE.sub(" ", str(text)).strip()
    if is_no_space_language(language):
        normalized = _SPACE_BETWEEN_WIDE.sub("", normalized)
    return normalized

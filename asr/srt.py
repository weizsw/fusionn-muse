"""Subtitle cue building and SRT output.

The cue structure is ported from ``mlx-qwen3-asr``'s ``writers.py``
(Apache-2.0, https://github.com/PRITHIVSAKTHIUR/mlx-qwen3-asr). Keeping the same
boundary rules (sentence, clause, pause, display width, balancing) and timestamp
format means a transcript written on a Linux route and one written on macOS come
out in the same shape, which the translation step downstream relies on.

Two deliberate differences from the ported code:

* the input is a list of :class:`Segment` instead of upstream's dictionaries,
  because both engines here already produce typed values;
* a cue for a no-space language keeps the separator between two Latin words, so
  an English term inside a Chinese transcript does not run together.
"""

from __future__ import annotations

import os
import re
import unicodedata
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Iterable, List, Optional, Sequence

from .languages import is_no_space_language, normalize_text

# Grouping defaults, taken from the ported implementation. They are per-cue
# limits for a screen reader, not model parameters.
MAX_WORDS = 10
MAX_CHARS = 42
MAX_DURATION_SEC = 6.0
MAX_GAP_SEC = 0.8


@dataclass(frozen=True)
class Segment:
    """One timed piece of transcript: a word, a character, or a finished cue."""

    text: str
    start: float
    end: float


def collapse(text: str) -> str:
    """One line of text with runs of whitespace collapsed."""
    return " ".join(str(text).split())


def format_timestamp(seconds: float) -> str:
    """Format seconds as an SRT timestamp: HH:MM:SS,mmm."""
    total_ms = max(0, int(round(seconds * 1000.0)))
    hours, rest_ms = divmod(total_ms, 3_600_000)
    minutes, rest_ms = divmod(rest_ms, 60_000)
    secs, millis = divmod(rest_ms, 1000)
    return f"{hours:02d}:{minutes:02d}:{secs:02d},{millis:03d}"


def sanitize(segments: Iterable[Segment]) -> List[Segment]:
    """Drop empty cues and keep the timeline ordered and free of overlap.

    A model can return items out of order or with a zero-length span; neither is
    useful to a player or to the translator, so the timeline is clamped here
    rather than in every writer.
    """
    cleaned: List[Segment] = []
    previous_end = 0.0
    for segment in segments:
        text = collapse(segment.text)
        if not text:
            continue
        start = max(0.0, float(segment.start), previous_end)
        end = max(float(segment.end), start)
        cleaned.append(Segment(text=text, start=start, end=end))
        previous_end = end
    return cleaned


def format_srt(segments: Sequence[Segment]) -> str:
    """The SRT text for the cues, or "" when there is nothing to write."""
    cues = sanitize(segments)
    if not cues:
        return ""
    blocks = [
        f"{index}\n{format_timestamp(cue.start)} --> {format_timestamp(cue.end)}\n{cue.text}"
        for index, cue in enumerate(cues, 1)
    ]
    return "\n\n".join(blocks) + "\n"


def write_srt(path: Path, segments: Sequence[Segment]) -> Optional[Path]:
    """Write the cues as SRT, or return None when there is nothing to write.

    The file is written next to the input and replaced in one step, so a reader
    (the Go client copies it away right after the response) never sees a
    half-written file.
    """
    text = format_srt(segments)
    if not text:
        return None
    target = Path(path)
    temporary = target.with_name(target.name + ".tmp")
    try:
        temporary.write_text(text, encoding="utf-8")
        os.replace(temporary, target)
    except OSError:
        # Leave no partial file behind; the original error is the one that matters.
        try:
            temporary.unlink()
        except OSError:
            pass
        raise
    return target


def group_segments(
    segments: Sequence[Segment],
    *,
    language: str = "",
    text: Optional[str] = None,
    max_words: int = MAX_WORDS,
    max_chars: int = MAX_CHARS,
    max_duration_sec: float = MAX_DURATION_SEC,
    max_gap_sec: float = MAX_GAP_SEC,
) -> List[Segment]:
    """Group word-level segments into subtitle-friendly cues.

    A cue ends at a sentence boundary, a pause of at least ``max_gap_sec``, or
    when adding the next word would exceed ``max_chars`` display cells (a wide
    or fullwidth character counts double) or ``max_duration_sec``. A clause
    boundary (comma, semicolon) also ends a cue once it is at least half full.
    ``max_words`` applies only to space-delimited languages, because the aligner
    emits one segment per character for Chinese and Japanese and a word cap
    would cut phrases at arbitrary points. Korean is wide-script but
    space-delimited, so it keeps spaces and the word cap.

    ``text`` is the full transcript: the forced aligner drops punctuation, so
    when it is given the punctuation is restored onto the segments first, which
    lets the sentence and clause boundaries above actually fire.
    """
    items: List[Segment] = []
    for segment in segments:
        text_value = collapse(segment.text)
        if not text_value:
            continue
        start = max(0.0, float(segment.start))
        items.append(Segment(text=text_value, start=start, end=max(float(segment.end), start)))
    if not items:
        return []
    if text:
        items = restore_punctuation(items, text)
    no_space = is_no_space_language(language)

    cues: List[List[Segment]] = []
    current: List[Segment] = []
    width_broken = set()  # indices of cues opened by a width overflow

    for item in items:
        if not current:
            current.append(item)
            continue

        current_text = _join_text(current, language)
        candidate_text = _join_text([*current, item], language)
        last_text = current[-1].text
        hard_break = (
            item.start - current[-1].end >= max_gap_sec
            or item.end - current[0].start > max_duration_sec
            or (not no_space and len(current) >= max_words)
            or _ends_sentence(last_text)
            or (_ends_clause(last_text) and _display_width(current_text) * 2 >= max_chars)
        )
        if hard_break or _display_width(candidate_text) > max_chars:
            cues.append(current)
            if not hard_break:
                width_broken.add(len(cues))
            current = [item]
        else:
            current.append(item)

    if current:
        cues.append(current)
    for index in sorted(width_broken):
        if 0 < index < len(cues):
            _balance_pair(cues[index - 1], cues[index], language=language, max_chars=max_chars)
    return [
        Segment(
            text=normalize_text(_join_text(tokens, language), language),
            start=float(tokens[0].start),
            end=float(tokens[-1].end),
        )
        for tokens in cues
    ]


def restore_punctuation(segments: Sequence[Segment], text: str) -> List[Segment]:
    """Re-attach the transcript's punctuation to aligner word segments.

    The forced aligner tokenizes the transcript into bare words (or CJK
    characters) and drops punctuation. This walks ``text`` alongside the
    segments and copies leading punctuation onto the first word of a phrase and
    trailing punctuation onto the last word. When the segments do not match the
    transcript character for character (ignoring case and whitespace), the input
    is returned unchanged rather than guessing.
    """
    length = len(text)
    index = 0
    restored: List[Segment] = []
    for segment in segments:
        characters = [char for char in segment.text if not char.isspace()]
        if not characters:
            restored.append(segment)
            continue
        leading: List[str] = []
        while index < length and (text[index].isspace() or _is_punctuation(text[index])):
            if not text[index].isspace():
                leading.append(text[index])
            index += 1
        for char in characters:
            if index < length and text[index].casefold() == char.casefold():
                index += 1
            else:
                return list(segments)
        trailing: List[str] = []
        while index < length and _is_punctuation(text[index]) and not _is_opening_punctuation(text[index]):
            trailing.append(text[index])
            index += 1
        restored.append(
            replace(
                segment,
                text="".join(leading) + segment.text.strip() + "".join(trailing),
            )
        )
    return restored


def _balance_pair(
    left: List[Segment], right: List[Segment], *, language: str, max_chars: int
) -> None:
    """Move trailing words from ``left`` to ``right`` when a width break orphaned a stub.

    A width overflow splits a phrase wherever the budget ran out, so the second
    cue can be a single word ("... over the lazy" / "dog."). Rebalance in place
    while the right cue is under a third of the budget and the move still leaves
    the left cue the wider of the two.
    """
    threshold = max_chars // 3

    def width(tokens: List[Segment]) -> int:
        return _display_width(_join_text(tokens, language))

    while len(left) > 1 and width(right) < threshold:
        moved = [left[-1], *right]
        if width(moved) > max_chars or width(left[:-1]) < width(moved):
            break
        right.insert(0, left.pop())


def _is_punctuation(char: str) -> bool:
    return unicodedata.category(char).startswith("P")


def _is_opening_punctuation(char: str) -> bool:
    return unicodedata.category(char) in {"Ps", "Pi"}


def _display_width(text: str) -> int:
    """Terminal-style display width: wide and fullwidth characters count double."""
    return sum(2 if unicodedata.east_asian_width(char) in ("W", "F") else 1 for char in text)


_CLOSING_PUNCTUATION = re.escape("\"'”’」』）)】》〉]")
_ENDS_SENTENCE = re.compile(r"[.!?。！？…][" + _CLOSING_PUNCTUATION + r"]*$")
_ENDS_CLAUSE = re.compile(r"[,;:，、；：][" + _CLOSING_PUNCTUATION + r"]*$")


def _ends_sentence(text: str) -> bool:
    return bool(_ENDS_SENTENCE.search(str(text or "").strip()))


def _ends_clause(text: str) -> bool:
    return bool(_ENDS_CLAUSE.search(str(text or "").strip()))


def _join_text(tokens: Sequence[Segment], language: str) -> str:
    parts = [collapse(token.text) for token in tokens]
    parts = [part for part in parts if part]
    if not parts:
        return ""
    if is_no_space_language(language):
        joined = _join_wide(parts)
    else:
        joined = " ".join(parts)
    # The ported rules: no space before closing punctuation, none after an
    # opening bracket.
    joined = re.sub(r"\s+([,.;:!?])", r"\1", joined)
    joined = re.sub(r"([(\[{])\s+", r"\1", joined)
    return joined.strip()


def _join_wide(parts: Sequence[str]) -> str:
    """Join tokens of a no-space language, separating only two Latin words."""
    joined = parts[0]
    for part in parts[1:]:
        if joined[-1].isascii() and part[0].isascii():
            joined += " "
        joined += part
    return joined

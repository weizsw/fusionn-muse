#!/usr/bin/env python3
"""Translate one subtitle file with PySubtrans."""

import argparse
import logging
import os
import re
import sys
import tempfile


AGE_SENSITIVE_RE = re.compile(
    r"(未成年|児童|子供|子ども|小学生|中学生|高校生|女子高生|JK|JC|JS|ロリ|ロリータ|少女|幼い|1[0-7]歳)",
    re.IGNORECASE,
)
SEXUAL_RE = re.compile(r"(エッチ|セックス|性交|性行為|挿入|中出し|射精|乳首|おっぱい|ちんこ|チンコ|まんこ)", re.IGNORECASE)
AGE_TERM_REPLACEMENT = "相手"


def sanitize_srt_for_translation(srt_text: str) -> str:
    blocks = srt_text.split("\n\n")
    parsed = []
    for block in blocks:
        lines = block.splitlines()
        cue_text = "\n".join(lines[2:])
        parsed.append((block, lines, len(lines) >= 3, AGE_SENSITIVE_RE.search(cue_text), SEXUAL_RE.search(cue_text)))

    sanitized = []
    for i, (block, lines, is_cue, has_age, has_sexual) in enumerate(parsed):
        prev = parsed[i - 1] if i > 0 else None
        next_ = parsed[i + 1] if i + 1 < len(parsed) else None
        neighbor_has_sexual = (prev and prev[4]) or (next_ and next_[4])
        if is_cue and has_age and (has_sexual or neighbor_has_sexual):
            sanitized.append("\n".join(lines[:2] + [AGE_SENSITIVE_RE.sub(AGE_TERM_REPLACEMENT, line) for line in lines[2:]]))
        else:
            sanitized.append(block)
    return "\n\n".join(sanitized)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Translate subtitles with PySubtrans")
    parser.add_argument("--input", required=True, help="Input subtitle path")
    parser.add_argument("--output", required=True, help="Output subtitle path")
    parser.add_argument("--target", required=True, help="Target language")
    parser.add_argument("--api-key", required=True, help="OpenAI-compatible API key")
    parser.add_argument("--base-url", required=True, help="OpenAI-compatible base URL")
    parser.add_argument("--model", required=True, help="Model name")
    parser.add_argument("--instruction", default="", help="Additional translation instruction")
    return parser.parse_args()


def main() -> int:
    args = parse_args()

    from PySubtrans import init_options, init_subtitles, init_translator

    logging.basicConfig(format="%(levelname)s: %(message)s", level=logging.INFO)

    instruction_args = [args.instruction] if args.instruction else None
    options = init_options(
        provider="OpenAI",
        api_key=args.api_key,
        api_base=args.base_url,
        model=args.model,
        target_language=args.target,
        instruction_args=instruction_args,
    )

    subtitle_input = args.input
    temp_input = None
    try:
        with open(args.input, encoding="utf-8") as f:
            raw_srt = f.read()
        sanitized_srt = sanitize_srt_for_translation(raw_srt)
        if sanitized_srt != raw_srt:
            with tempfile.NamedTemporaryFile("w", encoding="utf-8", suffix=".srt", delete=False) as f:
                f.write(sanitized_srt)
                temp_input = f.name
            subtitle_input = temp_input
            logging.info("Replaced age-sensitive subtitle terms before translation")

        subtitles = init_subtitles(subtitle_input, options=options)
        translator = init_translator(options)
        translator.events.connect_default_loggers()
        try:
            translator.TranslateSubtitles(subtitles)
        finally:
            translator.events.disconnect_default_loggers()
        subtitles.SaveTranslation(args.output)
    finally:
        if temp_input:
            os.unlink(temp_input)
    return 0


if __name__ == "__main__":
    sys.exit(main())

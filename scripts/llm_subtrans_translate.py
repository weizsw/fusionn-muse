#!/usr/bin/env python3
"""Translate one subtitle file with PySubtrans."""

import argparse
import logging
import math
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


def non_negative_rate(value: str) -> float:
    rate = float(value)
    if not math.isfinite(rate) or rate < 0:
        raise argparse.ArgumentTypeError("Rate limit must be a finite non-negative number")
    return rate


def non_negative_float(value: str) -> float:
    val = float(value)
    if not math.isfinite(val) or val < 0:
        raise argparse.ArgumentTypeError("Value must be a finite non-negative number")
    return val


def positive_retries(value: str) -> int:
    retries = int(value)
    if retries < 1:
        raise argparse.ArgumentTypeError("Max retries must be at least 1")
    return retries


def positive_timeout(value: str) -> int:
    timeout = int(value)
    if timeout < 1:
        raise argparse.ArgumentTypeError("Request timeout must be at least 1 second")
    return timeout


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Translate subtitles with PySubtrans")
    parser.add_argument("--input", required=True, help="Input subtitle path")
    parser.add_argument("--output", required=True, help="Output subtitle path")
    parser.add_argument("--target", required=True, help="Target language")
    parser.add_argument("--api-key", required=True, help="OpenAI-compatible API key")
    parser.add_argument("--base-url", required=True, help="OpenAI-compatible base URL")
    parser.add_argument("--model", required=True, help="Model name")
    parser.add_argument("--instruction", default="", help="Additional translation instruction")
    parser.add_argument("--ratelimit", type=non_negative_rate, default=0.0, help="Translation requests per minute (0 disables pacing)")
    parser.add_argument("--max-retries", type=positive_retries, default=1, help="Retries after the initial batch request (minimum/default 1)")
    parser.add_argument("--request-timeout", type=positive_timeout, default=180, help="HTTP request timeout in seconds (minimum 1, default 180)")
    parser.add_argument("--retry-backoff", type=non_negative_float, default=3.0, help="Initial retry delay in seconds (0 disables waiting, default 3.0)")
    return parser.parse_args()


def main() -> int:
    args = parse_args()

    from PySubtrans import init_options, init_subtitles, init_translation_provider, init_translator

    # CustomClient 1.7.3 uses `self.settings.get_float('backoff_time') or 5.0`.
    # When backoff_time is 0.0, the `or 5.0` falsy fallback turns 0 into 5.0 seconds.
    # Preserve explicit 0.0 so clients with external fallback chains can retry immediately.
    try:
        from PySubtrans.Providers.Clients.CustomClient import CustomClient

        CustomClient.backoff_time = property(
            lambda self: self.settings.get_float("backoff_time")
            if self.settings.get("backoff_time") is not None
            else 3.0
        )
    except (ImportError, AttributeError):
        pass

    logging.basicConfig(format="%(levelname)s: %(message)s", level=logging.INFO)

    instruction_args = [args.instruction] if args.instruction else None
    options = init_options(
        provider="Custom Server",
        server_address=args.base_url.rstrip("/"),
        endpoint="/chat/completions",
        supports_conversation=True,
        supports_system_messages=True,
        stream_responses=False,
        timeout=args.request_timeout,
        max_retries=args.max_retries,
        backoff_time=args.retry_backoff,
        api_key=args.api_key,
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
        translation_provider = None
        if args.ratelimit > 0:
            translation_provider = init_translation_provider("Custom Server", options)
            # Custom Server 1.7.3 drops the top-level rate_limit option. Its
            # client inherits these provider settings and applies batch pacing.
            translation_provider.settings["rate_limit"] = args.ratelimit
            translation_provider.settings["supports_parallel_threads"] = False
        translator = init_translator(options, translation_provider=translation_provider)
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

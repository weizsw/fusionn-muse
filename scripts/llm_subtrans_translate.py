#!/usr/bin/env python3
"""Translate one subtitle file with PySubtrans."""

import argparse
import logging
import sys


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

    subtitles = init_subtitles(args.input, options=options)
    translator = init_translator(options)
    translator.events.connect_default_loggers()
    try:
        translator.TranslateSubtitles(subtitles)
    finally:
        translator.events.disconnect_default_loggers()
    subtitles.SaveTranslation(args.output)
    return 0


if __name__ == "__main__":
    sys.exit(main())

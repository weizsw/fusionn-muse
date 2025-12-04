#!/usr/bin/env python3
"""
Subtitle post-processing using VideoCaptioner's core modules.
Includes subtitle correction, sentence splitting, and translation.

Usage: python subtitle_processor.py <input.srt> <output.srt> [options]
"""

import argparse
import os
import sys

# VideoCaptioner core imports (from /app/videocaptioner)
from app.core.asr.asr_data import ASRData
from app.core.optimize.optimize import SubtitleOptimizer
from app.core.split.split import SubtitleSplitter
from app.core.translate.llm_translator import LLMTranslator
from app.core.translate.types import TargetLanguage


def setup_env(api_key: str, base_url: str):
    """Set environment variables for VideoCaptioner's LLM client."""
    os.environ["OPENAI_API_KEY"] = api_key
    os.environ["OPENAI_BASE_URL"] = base_url




def main():
    parser = argparse.ArgumentParser(
        description="Process subtitles using VideoCaptioner"
    )
    parser.add_argument("input", help="Input SRT file")
    parser.add_argument("output", help="Output SRT file")

    # LLM settings
    parser.add_argument(
        "--api-key", "-k", default=os.getenv("OPENAI_API_KEY", ""), help="API key"
    )
    parser.add_argument(
        "--base-url",
        "-u",
        default=os.getenv("OPENAI_BASE_URL", "https://api.openai.com"),
        help="API base URL",
    )
    parser.add_argument("--model", "-m", default="gpt-4o-mini", help="LLM model")

    # Processing options
    parser.add_argument(
        "--optimize", action="store_true", help="Enable LLM subtitle correction"
    )
    parser.add_argument(
        "--split", action="store_true", help="Enable LLM sentence splitting"
    )
    parser.add_argument(
        "--translate",
        help="Target language for translation (e.g., 'Simplified Chinese')",
    )
    parser.add_argument(
        "--reflect",
        action="store_true",
        help="Use reflection translation (higher quality)",
    )

    # Split settings
    parser.add_argument(
        "--max-cjk", type=int, default=25, help="Max CJK characters per line"
    )
    parser.add_argument(
        "--max-english", type=int, default=18, help="Max English words per line"
    )

    # Post-processing
    parser.add_argument(
        "--remove-punctuation", action="store_true", help="Remove trailing punctuation"
    )

    # Reference/instruction
    parser.add_argument(
        "--reference", "-r", default="", help="Reference content for optimization"
    )

    # Concurrency
    parser.add_argument("--threads", type=int, default=4, help="Number of threads")
    parser.add_argument("--batch-size", type=int, default=10, help="Subtitles per batch")

    args = parser.parse_args()

    if not os.path.exists(args.input):
        print(f"Error: Input file not found: {args.input}", file=sys.stderr)
        sys.exit(1)

    if (args.optimize or args.split or args.translate) and not args.api_key:
        print("Error: API key required for LLM processing", file=sys.stderr)
        sys.exit(1)

    # Setup environment for VideoCaptioner's LLM client
    if args.api_key:
        setup_env(args.api_key, args.base_url)

    # Load subtitle
    print(f"Loading: {args.input}", flush=True)
    asr_data = ASRData.from_subtitle_file(args.input)
    print(f"Loaded {len(asr_data.segments)} segments", flush=True)

    # Step 1: Split sentences (merge word-level → sentence-level)
    # Must happen before optimize since optimize works on sentences, not words
    if args.split:
        print(f"Splitting sentences with {args.model}...", flush=True)
        splitter = SubtitleSplitter(
            thread_num=args.threads,
            model=args.model,
            max_word_count_cjk=args.max_cjk,
            max_word_count_english=args.max_english,
        )
        try:
            asr_data = splitter.split_subtitle(asr_data)
            print(f"Split complete: {len(asr_data.segments)} segments", flush=True)
        except Exception as e:
            print(f"Warning: Split failed: {e}", flush=True)
        finally:
            splitter.stop()

    # Step 2: Optimize (fix recognition errors) - works on sentence-level
    if args.optimize:
        print(f"Optimizing subtitles with {args.model}...", flush=True)
        optimizer = SubtitleOptimizer(
            thread_num=args.threads,
            batch_num=args.batch_size,
            model=args.model,
            custom_prompt=args.reference,
        )
        try:
            asr_data = optimizer.optimize_subtitle(asr_data)
            print(
                f"Optimization complete: {len(asr_data.segments)} segments", flush=True
            )
        except Exception as e:
            print(f"Warning: Optimization failed: {e}", flush=True)
        finally:
            optimizer.stop()

    # Step 3: Remove trailing punctuation
    if args.remove_punctuation:
        print("Removing trailing punctuation...", flush=True)
        asr_data.remove_punctuation()

    # Step 4: Translate
    if args.translate:
        print(f"Translating to {args.translate} with {args.model}...", flush=True)

        # Map language string to enum
        lang_map = {
            "simplified chinese": TargetLanguage.SIMPLIFIED_CHINESE,
            "traditional chinese": TargetLanguage.TRADITIONAL_CHINESE,
            "english": TargetLanguage.ENGLISH,
            "japanese": TargetLanguage.JAPANESE,
            "korean": TargetLanguage.KOREAN,
            "french": TargetLanguage.FRENCH,
            "german": TargetLanguage.GERMAN,
            "spanish": TargetLanguage.SPANISH,
            "russian": TargetLanguage.RUSSIAN,
        }
        target_lang = lang_map.get(
            args.translate.lower(), TargetLanguage.SIMPLIFIED_CHINESE
        )

        translator = LLMTranslator(
            thread_num=args.threads,
            batch_num=10,
            target_language=target_lang,
            model=args.model,
            custom_prompt=args.reference,
            is_reflect=args.reflect,
            update_callback=None,
        )
        try:
            asr_data = translator.translate_subtitle(asr_data)
            print(
                f"Translation complete: {len(asr_data.segments)} segments", flush=True
            )
        except Exception as e:
            print(f"Warning: Translation failed: {e}", flush=True)
        finally:
            translator.stop()

    # Save output
    asr_data.to_srt(args.output)
    print(f"Saved: {args.output} ({len(asr_data.segments)} segments)", flush=True)


if __name__ == "__main__":
    main()

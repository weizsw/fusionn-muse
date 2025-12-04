#!/usr/bin/env python3
"""
Translate subtitles using VideoCaptioner's LLM translator.
Uses batch processing with validation/retry for higher quality.

Usage: python translate.py <input.srt> <output.srt> --target "Simplified Chinese" [options]
"""

import argparse
import os
import sys

# VideoCaptioner core imports
from app.core.asr.asr_data import ASRData
from app.core.entities import SubtitleLayoutEnum
from app.core.translate.llm_translator import LLMTranslator
from app.core.translate.types import TargetLanguage


# Language name to enum mapping
LANGUAGE_MAP = {
    # Chinese
    "simplified chinese": TargetLanguage.SIMPLIFIED_CHINESE,
    "traditional chinese": TargetLanguage.TRADITIONAL_CHINESE,
    "简体中文": TargetLanguage.SIMPLIFIED_CHINESE,
    "繁体中文": TargetLanguage.TRADITIONAL_CHINESE,
    # English
    "english": TargetLanguage.ENGLISH,
    "english (us)": TargetLanguage.ENGLISH_US,
    "english (uk)": TargetLanguage.ENGLISH_UK,
    # Asian
    "japanese": TargetLanguage.JAPANESE,
    "日本語": TargetLanguage.JAPANESE,
    "korean": TargetLanguage.KOREAN,
    "韩语": TargetLanguage.KOREAN,
    "cantonese": TargetLanguage.CANTONESE,
    "粤语": TargetLanguage.CANTONESE,
    "thai": TargetLanguage.THAI,
    "vietnamese": TargetLanguage.VIETNAMESE,
    "indonesian": TargetLanguage.INDONESIAN,
    "malay": TargetLanguage.MALAY,
    "tagalog": TargetLanguage.TAGALOG,
    # European
    "french": TargetLanguage.FRENCH,
    "german": TargetLanguage.GERMAN,
    "spanish": TargetLanguage.SPANISH,
    "spanish (latam)": TargetLanguage.SPANISH_LATAM,
    "russian": TargetLanguage.RUSSIAN,
    "portuguese": TargetLanguage.PORTUGUESE,
    "portuguese (brazil)": TargetLanguage.PORTUGUESE_BR,
    "portuguese (portugal)": TargetLanguage.PORTUGUESE_PT,
    "italian": TargetLanguage.ITALIAN,
    "dutch": TargetLanguage.DUTCH,
    "polish": TargetLanguage.POLISH,
    "turkish": TargetLanguage.TURKISH,
    "greek": TargetLanguage.GREEK,
    "czech": TargetLanguage.CZECH,
    "swedish": TargetLanguage.SWEDISH,
    "danish": TargetLanguage.DANISH,
    "finnish": TargetLanguage.FINNISH,
    "norwegian": TargetLanguage.NORWEGIAN,
    "hungarian": TargetLanguage.HUNGARIAN,
    "romanian": TargetLanguage.ROMANIAN,
    "bulgarian": TargetLanguage.BULGARIAN,
    "ukrainian": TargetLanguage.UKRAINIAN,
    # Middle Eastern
    "arabic": TargetLanguage.ARABIC,
    "hebrew": TargetLanguage.HEBREW,
    "persian": TargetLanguage.PERSIAN,
}


def get_target_language(lang_str: str) -> TargetLanguage:
    """Convert language string to TargetLanguage enum."""
    key = lang_str.lower().strip()
    if key in LANGUAGE_MAP:
        return LANGUAGE_MAP[key]

    # Fuzzy match
    for k, v in LANGUAGE_MAP.items():
        if key in k or k in key:
            return v

    print(f"Warning: Unknown language '{lang_str}', defaulting to Simplified Chinese", file=sys.stderr)
    return TargetLanguage.SIMPLIFIED_CHINESE


def setup_env(api_key: str, base_url: str):
    """Set environment variables for VideoCaptioner's LLM client."""
    os.environ["OPENAI_API_KEY"] = api_key
    os.environ["OPENAI_BASE_URL"] = base_url


def translate(
    input_path: str,
    output_path: str,
    target_language: TargetLanguage,
    model: str = "gpt-4o-mini",
    reflect: bool = False,
    threads: int = 4,
    batch_size: int = 10,
    prompt: str = "",
) -> int:
    """Translate subtitle file.

    Returns:
        Number of translated segments.
    """
    # Load subtitle
    print(f"Loading: {os.path.basename(input_path)}", flush=True)
    asr_data = ASRData.from_subtitle_file(input_path)
    segment_count = len(asr_data.segments)
    print(f"Loaded {segment_count} segments", flush=True)

    if segment_count == 0:
        print("Warning: No segments to translate", file=sys.stderr)
        return 0

    # Create translator
    mode = "reflect" if reflect else "standard"
    print(f"Translating → {target_language.value} ({mode} mode, {threads} threads)", flush=True)

    translator = LLMTranslator(
        thread_num=threads,
        batch_num=batch_size,
        target_language=target_language,
        model=model,
        custom_prompt=prompt,
        is_reflect=reflect,
        update_callback=lambda chunk: print(
            f"  ✓ Translated batch: {chunk[0].index}-{chunk[-1].index}", flush=True
        ),
    )

    try:
        asr_data = translator.translate_subtitle(asr_data)
        print(f"Translation complete: {len(asr_data.segments)} segments", flush=True)
    finally:
        translator.stop()

    # Remove trailing punctuation (like VideoCaptioner does after translation)
    asr_data.remove_punctuation()

    # Save output (translated only, not bilingual)
    asr_data.to_srt(layout=SubtitleLayoutEnum.ONLY_TRANSLATE, save_path=output_path)
    print(f"Saved: {os.path.basename(output_path)}", flush=True)

    return len(asr_data.segments)


def main():
    parser = argparse.ArgumentParser(
        description="Translate subtitles using VideoCaptioner's LLM translator"
    )
    parser.add_argument("input", help="Input SRT file")
    parser.add_argument("output", help="Output SRT file")

    # Required
    parser.add_argument(
        "--target", "-t",
        required=True,
        help="Target language (e.g., 'Simplified Chinese', 'English', 'Japanese')",
    )

    # LLM settings
    parser.add_argument(
        "--api-key", "-k",
        default=os.getenv("OPENAI_API_KEY", ""),
        help="API key (default: OPENAI_API_KEY env var)",
    )
    parser.add_argument(
        "--base-url", "-u",
        default=os.getenv("OPENAI_BASE_URL", "https://api.openai.com"),
        help="API base URL (default: OPENAI_BASE_URL env var)",
    )
    parser.add_argument(
        "--model", "-m",
        default="gpt-4o-mini",
        help="LLM model (default: gpt-4o-mini)",
    )

    # Translation options
    parser.add_argument(
        "--reflect", "-r",
        action="store_true",
        help="Use reflection mode (higher quality, slower)",
    )
    parser.add_argument(
        "--prompt", "-p",
        default="",
        help="Custom prompt/instructions for translation (e.g., terminology, style)",
    )

    # Performance
    parser.add_argument(
        "--threads",
        type=int,
        default=4,
        help="Number of parallel threads (default: 4)",
    )
    parser.add_argument(
        "--batch-size",
        type=int,
        default=10,
        help="Batch size for translation (default: 10)",
    )

    args = parser.parse_args()

    # Validate input
    if not os.path.exists(args.input):
        print(f"Error: Input file not found: {args.input}", file=sys.stderr)
        sys.exit(1)

    if not args.api_key:
        print("Error: API key required (--api-key or OPENAI_API_KEY env var)", file=sys.stderr)
        sys.exit(1)

    # Setup environment
    setup_env(args.api_key, args.base_url)

    # Get target language
    target_lang = get_target_language(args.target)

    try:
        count = translate(
            args.input,
            args.output,
            target_lang,
            model=args.model,
            reflect=args.reflect,
            threads=args.threads,
            batch_size=args.batch_size,
            prompt=args.prompt,
        )
        if count == 0:
            sys.exit(1)
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()


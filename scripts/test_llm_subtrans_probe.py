#!/usr/bin/env python3
"""Differential test: the translation failure verdict must not change with the
log format.

The previous release configured basicConfig(level=INFO) with
"%(levelname)s: %(message)s" on stderr, and the caller reports a failed
translation by searching that stream for "Error:" or "Traceback". The unified
format, the configurable level and the content limits all change what reaches
stderr, so this test runs the previous script and the current one with the same
library behaviour, at each level, and compares the two verdicts.

The current verdict mirrors detectScriptFailure in
internal/executor/llm_subtrans.go, which has its own table test.
"""

import hashlib
import os
import pathlib
import re
import subprocess
import sys
import tempfile
import textwrap
import unittest

ROOT = pathlib.Path(__file__).resolve().parent.parent
SCRIPTS = ROOT / "scripts"

# The pre-migration script is a fixture, byte for byte as it was at 8d6dd91:
#     git show 8d6dd91:scripts/llm_subtrans_translate.py
# The file name does not end in .py, so test discovery never runs it as a test
# module; the suite reads it from disk and runs it from a temporary copy, so a
# shallow clone or a source archive runs this suite with no git history.
BASE_REVISION = "8d6dd91"
PREVIOUS_SCRIPT = SCRIPTS / "testdata" / "previous_llm_subtrans_translate-8d6dd91.txt"
PREVIOUS_SCRIPT_SHA256 = "f2f06c983d7861075acc65787f95623b1df97500023ccc1cdadd3dffd7a15f4a"

UNIFIED_LINE = re.compile(
    r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}[+-]\d{2}:\d{2} "
    r"(DEBUG|INFO|WARN|ERROR) service=fusionn-muse component=llm-subtrans event=(\S+)(?: |$)"
)

SCRIPT_FAILURE_EVENT = "script_failure_detected"


def legacy_verdict(stderr):
    """The caller's original check, unchanged."""
    return "Error:" in stderr or "Traceback" in stderr


def current_verdict(stderr):
    """Mirror of the scriptCapture verdict in internal/executor/scriptcapture.go.

    A line boundary is an LF or a CR, only the script's own service and
    component are read as its display output, and the control event counts only
    in the header of such a line.
    """
    for line in re.split(r"[\r\n]", stderr):
        match = UNIFIED_LINE.match(line)
        if match:
            if match.group(2) == SCRIPT_FAILURE_EVENT:
                return True
            continue
        if "Error:" in line or "Traceback" in line:
            return True
    return False


# Each scenario is the body of TranslateSubtitles in the stub provider.
SCENARIOS = {
    # A third-party INFO record whose matching text sits past any excerpt limit.
    "info_marker_after_600": "log.info('x' * 600 + ' Error: upstream failure')",
    # The library handles the failure itself and keeps going.
    "handled_exception": (
        "try:\n"
        "    raise RuntimeError('handled upstream')\n"
        "except RuntimeError:\n"
        "    log.error('Translation request failed', exc_info=True)"
    ),
    # A normal error whose text matches nothing.
    "plain_error": "log.error('Translation request retried')",
    # Only visible at DEBUG, which the previous release never wrote.
    "debug_with_markers": "log.debug('Error: Traceback detail')",
    # Raw output that never went through logging, ending without a newline.
    "raw_tail_marker": "sys.stderr.write('noise\\nError: raw tail')",
    # A warning the library raised: the previous release printed it straight to
    # stderr, with the marker in its body.
    "warning_with_marker": "warnings.warn('upstream Error: retry later')",
    # A library leaves a partial line with no separator. The probe hit is an
    # INFO record, which an ERROR level hides from the display stream, so only
    # the line boundary before the control record can carry the verdict.
    "unseparated_prefix_with_hidden_probe": (
        "sys.stderr.write('progress 100%')\n"
        "log.info('Error: upstream retry')"
    ),
    "raw_tail_clean": "sys.stderr.write('noise\\nclean raw tail')",
}

PROVIDER = '''
import logging
import sys
import warnings

log = logging.getLogger("PySubtrans.Translator")


class _Events:
    def connect_default_loggers(self):
        pass

    def disconnect_default_loggers(self):
        pass


class _Subtitles:
    def SaveTranslation(self, path):
        with open(path, "w", encoding="utf-8") as handle:
            handle.write("1\\n00:00:01,000 --> 00:00:02,000\\ntranslated\\n")


class _Translator:
    def __init__(self):
        self.events = _Events()

    def TranslateSubtitles(self, subtitles):
{scenario}


def init_options(**kwargs):
    return kwargs


def init_subtitles(path, options=None):
    return _Subtitles()


def init_translation_provider(name, options):
    return object()


def init_translator(options, translation_provider=None):
    return _Translator()
'''

SRT = "1\n00:00:01,000 --> 00:00:02,000\nHello\n\n"


def previous_script(destination):
    """The file as it was before the log format changed."""
    content = PREVIOUS_SCRIPT.read_bytes()
    digest = hashlib.sha256(content).hexdigest()
    if digest != PREVIOUS_SCRIPT_SHA256:
        raise AssertionError(
            "the pre-migration fixture changed: sha256 %s, want %s (from %s)"
            % (digest, PREVIOUS_SCRIPT_SHA256, BASE_REVISION)
        )
    destination.write_bytes(content)
    return destination


def run_script(script, scenario, level, directory):
    stubs = directory / "stubs"
    package = stubs / "PySubtrans"
    package.mkdir(parents=True, exist_ok=True)
    (package / "__init__.py").write_text(PROVIDER.format(scenario=textwrap.indent(SCENARIOS[scenario], " " * 8)))

    source = directory / "input.srt"
    source.write_text(SRT)
    output = directory / ("out-%s-%s.srt" % (scenario, level))

    environment = {
        **os.environ,
        "PYTHONPATH": str(stubs),
        "PYTHONDONTWRITEBYTECODE": "1",
        "LOG_LEVEL": level,
    }
    result = subprocess.run(
        [
            sys.executable, "-B", str(script),
            "--input", str(source),
            "--output", str(output),
            "--target", "Simplified Chinese",
            "--api-key", "TEST_ONLY_KEY",
            "--base-url", "http://example.invalid",
            "--model", "test-model",
        ],
        capture_output=True,
        text=True,
        timeout=60,
        env=environment,
    )
    return result, output


class VerdictDifferentialTest(unittest.TestCase):
    def test_the_verdict_matches_the_previous_release_at_every_level(self):
        for scenario in SCENARIOS:
            for level in ("DEBUG", "INFO", "ERROR"):
                with self.subTest(scenario=scenario, level=level):
                    with tempfile.TemporaryDirectory() as raw:
                        directory = pathlib.Path(raw)
                        old_dir = directory / "old"
                        new_dir = directory / "new"
                        old_dir.mkdir()
                        new_dir.mkdir()

                        old, old_output = run_script(previous_script(old_dir / "old.py"), scenario, level, old_dir)
                        new, new_output = run_script(SCRIPTS / "llm_subtrans_translate.py", scenario, level, new_dir)

                        oracle = legacy_verdict(old.stderr)
                        self.assertEqual(old.returncode, 0, "the fixture itself must succeed: " + old.stderr)
                        self.assertTrue(old_output.exists(), "the fixture must produce a valid output file")
                        self.assertEqual(new.returncode, 0, new.stderr)
                        self.assertTrue(new_output.is_file() and new_output.stat().st_size, new.stderr)
                        self.assertEqual(new.stdout, old.stdout, "stdout must not change")
                        self.assertEqual(
                            current_verdict(new.stderr),
                            oracle,
                            "verdict changed\n--- old stderr ---\n%s\n--- new stderr ---\n%s" % (old.stderr, new.stderr),
                        )

    def test_info_record_marker_after_600_characters_still_fails(self):
        """The old stream carried the whole record; an excerpt limit must not hide it."""
        self.assertTrue(legacy_verdict("INFO: " + "x" * 600 + " Error: upstream failure"))

    def test_a_normalized_debug_preview_never_fails_a_successful_run(self):
        line = (
            "2026-07-01T14:20:03.456+08:00 DEBUG service=fusionn-muse component=llm-subtrans "
            'event=llm_response_preview preview="Error: Traceback inside a kept excerpt" '
            'msg="LLM response preview"'
        )
        self.assertTrue(legacy_verdict(line), "the raw text would have failed the old check")
        self.assertFalse(current_verdict(line), "a normalized line must not be searched for failure text")

    def test_unformatted_output_still_uses_the_previous_predicate(self):
        self.assertTrue(current_verdict("Error: raw tail"))
        self.assertTrue(current_verdict("Traceback (most recent call last):"))
        self.assertFalse(current_verdict("clean raw tail"))

    def test_the_control_event_is_read_from_our_own_header(self):
        record = (
            "2026-07-01T14:20:03.456+08:00 ERROR service=fusionn-muse component=llm-subtrans "
            "event=" + SCRIPT_FAILURE_EVENT + ' msg="Script output matched"'
        )
        self.assertTrue(current_verdict(record), "our own control record must fail the run")
        # A bare token, another service, or a quoted value never forges the event.
        self.assertFalse(current_verdict("x\n" + SCRIPT_FAILURE_EVENT + ' msg="matched"'))
        self.assertFalse(
            current_verdict(record.replace("component=llm-subtrans", "component=translator"))
        )
        self.assertFalse(
            current_verdict(record.replace("event=" + SCRIPT_FAILURE_EVENT, "event=log"))
        )


if __name__ == "__main__":
    unittest.main()

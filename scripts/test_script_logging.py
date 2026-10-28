#!/usr/bin/env python3
"""Entry-point contract of our own scripts: stdout stays the result, stderr is one
unified line per event, and the exit code is unchanged."""

import os
import pathlib
import re
import subprocess
import sys
import tempfile
import unittest


SCRIPTS = pathlib.Path(__file__).parent
UNIFIED = re.compile(
    r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}\+08:00 "
    r"(DEBUG|INFO|WARN|ERROR) component=\S+ event=\S+ "
    r'.*msg=".*"$'
)

CV2_STUB = """
CAP_PROP_FRAME_COUNT = 1
CAP_PROP_FPS = 2
CAP_PROP_POS_FRAMES = 3


class VideoCapture:
    def __init__(self, _path):
        pass

    def get(self, _prop):
        return 0

    def isOpened(self):
        return False

    def release(self):
        pass
"""

RAPIDOCR_STUB = """
class RapidOCR:
    pass
"""


def run(script, argv, path_extra=None):
    environment = {**os.environ, "PYTHONDONTWRITEBYTECODE": "1"}
    if path_extra:
        environment["PYTHONPATH"] = path_extra
    return subprocess.run(
        [sys.executable, "-B", str(SCRIPTS / script), *argv],
        capture_output=True,
        text=True,
        timeout=60,
        env=environment,
    )


class OwnScriptLoggingTest(unittest.TestCase):
    def assert_unified_stderr(self, result):
        # A blank line is a boundary separator before a final record, never an
        # event: every non-empty line is one unified event.
        lines = [line for line in result.stderr.strip().splitlines() if line]
        self.assertTrue(lines)
        for line in lines:
            self.assertRegex(line, UNIFIED)

    def test_detect_hard_sub_keeps_stdout_for_the_result_and_reports_failure_once(self):
        with tempfile.TemporaryDirectory() as stubs:
            pathlib.Path(stubs, "cv2.py").write_text(CV2_STUB)
            pathlib.Path(stubs, "rapidocr.py").write_text(RAPIDOCR_STUB)
            result = run("detect_hard_sub.py", ["movie.mp4"], path_extra=stubs)

        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stdout, "", "stdout must stay the boolean result channel")
        self.assert_unified_stderr(result)
        self.assertIn("event=ocr_failed", result.stderr)
        self.assertIn("ERROR", result.stderr)
        self.assertIn('error="RuntimeError"', result.stderr)
        # Diagnostics survive as the call frames, not as the exception text.
        self.assertIn("stack=", result.stderr)
        self.assertIn("detect_hard_sub.py", result.stderr)

    def test_llm_subtrans_keeps_stdout_empty_and_keeps_the_failure_signal(self):
        result = run(
            "llm_subtrans_translate.py",
            [
                "--input", "/nonexistent.srt",
                "--output", "/tmp/never-written.srt",
                "--target", "Simplified Chinese",
                "--api-key", "TEST_ONLY_KEY",
                "--base-url", "http://example.invalid",
                "--model", "test-model",
            ],
        )

        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stdout, "")
        self.assert_unified_stderr(result)
        self.assertIn("event=translation_failed", result.stderr)
        self.assertIn('error="ModuleNotFoundError"', result.stderr)
        self.assertIn("module=PySubtrans", result.stderr)
        # The verdict the caller acts on is the constant event, not a text match.
        self.assertIn("event=script_failure_detected", result.stderr)

    def test_llm_subtrans_does_not_log_the_api_key(self):
        result = run(
            "llm_subtrans_translate.py",
            [
                "--input", "/nonexistent.srt",
                "--output", "/tmp/never-written.srt",
                "--target", "Simplified Chinese",
                "--api-key", "TEST_ONLY_SECRET_VALUE",
                "--base-url", "https://user:TEST_ONLY_SECRET_VALUE@example.invalid",
                "--model", "test-model",
            ],
        )

        self.assertNotIn("TEST_ONLY_SECRET_VALUE", result.stderr)
        self.assertNotIn("TEST_ONLY_SECRET_VALUE", result.stdout)


if __name__ == "__main__":
    unittest.main()

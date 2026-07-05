import importlib.util
import pathlib
import sys
import tempfile
import types
import unittest
from unittest import mock


SCRIPT = pathlib.Path(__file__).with_name("llm_subtrans_translate.py")
spec = importlib.util.spec_from_file_location("llm_subtrans_translate", SCRIPT)
llm_subtrans_translate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(llm_subtrans_translate)


class SanitizeSRTForTranslationTest(unittest.TestCase):
    def test_redacts_age_sensitive_sexual_cue(self):
        srt = """1
00:00:01,000 --> 00:00:02,000
今日は雨ですね。

2
00:00:03,000 --> 00:00:04,000
JKとエッチしたい。
"""

        sanitized = llm_subtrans_translate.sanitize_srt_for_translation(srt)

        self.assertIn("今日は雨ですね。", sanitized)
        self.assertIn("00:00:03,000 --> 00:00:04,000", sanitized)
        self.assertIn("相手とエッチしたい。", sanitized)
        self.assertNotIn("JK", sanitized)

    def test_redacts_adjacent_age_sensitive_and_sexual_cues(self):
        srt = """1
00:00:01,000 --> 00:00:02,000
JKです。

2
00:00:03,000 --> 00:00:04,000
エッチしたい。
"""

        sanitized = llm_subtrans_translate.sanitize_srt_for_translation(srt)

        self.assertIn("00:00:01,000 --> 00:00:02,000", sanitized)
        self.assertIn("00:00:03,000 --> 00:00:04,000", sanitized)
        self.assertIn("相手です。", sanitized)
        self.assertIn("エッチしたい。", sanitized)
        self.assertNotIn("JK", sanitized)

    def test_main_sends_sanitized_subtitles_to_pysubtrans(self):
        srt = """1
00:00:01,000 --> 00:00:02,000
JKとエッチしたい。
"""
        captured = {}

        class FakeEvents:
            def connect_default_loggers(self):
                pass

            def disconnect_default_loggers(self):
                pass

        class FakeTranslator:
            events = FakeEvents()

            def TranslateSubtitles(self, subtitles):
                captured["input"] = pathlib.Path(subtitles.path).read_text()

        class FakeSubtitles:
            def __init__(self, path):
                self.path = path

            def SaveTranslation(self, output):
                pathlib.Path(output).write_text("ok")

        fake_pysubtrans = types.ModuleType("PySubtrans")
        fake_pysubtrans.init_options = lambda **kwargs: object()
        fake_pysubtrans.init_subtitles = lambda path, options=None: FakeSubtitles(path)
        fake_pysubtrans.init_translator = lambda options: FakeTranslator()

        with tempfile.TemporaryDirectory() as tmp:
            input_path = pathlib.Path(tmp, "input.srt")
            output_path = pathlib.Path(tmp, "output.srt")
            input_path.write_text(srt)
            argv = [
                "llm_subtrans_translate.py",
                "--input",
                str(input_path),
                "--output",
                str(output_path),
                "--target",
                "Simplified Chinese",
                "--api-key",
                "test",
                "--base-url",
                "http://example.test/v1",
                "--model",
                "test-model",
            ]

            with mock.patch.dict(sys.modules, {"PySubtrans": fake_pysubtrans}), mock.patch.object(sys, "argv", argv):
                self.assertEqual(llm_subtrans_translate.main(), 0)

        self.assertIn("相手とエッチしたい。", captured["input"])
        self.assertNotIn("JK", captured["input"])


if __name__ == "__main__":
    unittest.main()

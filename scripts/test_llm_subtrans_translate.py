import importlib.util
import io
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


class CustomServerTranslationTest(unittest.TestCase):
    def test_main_configures_chat_requests_with_bounded_retries(self):
        for base_url in [
            "http://example.test/v1",
            "http://example.test/v1/",
            "http://example.test/proxy/v1",
            "http://example.test",
        ]:
            with self.subTest(base_url=base_url), tempfile.TemporaryDirectory() as tmp:
                input_path = pathlib.Path(tmp, "input.srt")
                input_path.write_text("1\n00:00:01,000 --> 00:00:02,000\nHello.\n")
                fake_pysubtrans = types.ModuleType("PySubtrans")
                fake_pysubtrans.init_options = mock.Mock(return_value=object())
                fake_pysubtrans.init_subtitles = mock.Mock()
                fake_pysubtrans.init_translation_provider = mock.Mock()
                fake_pysubtrans.init_translator = mock.Mock()
                argv = [
                    "llm_subtrans_translate.py",
                    "--input", str(input_path),
                    "--output", str(pathlib.Path(tmp, "output.srt")),
                    "--target", "Simplified Chinese",
                    "--api-key", "test-key",
                    "--base-url", base_url,
                    "--model", "grok-4.5",
                    "--instruction", "Use natural spoken Chinese.",
                ]
                for config_args, expected_kwargs in [
                    ((), {"timeout": 180, "max_retries": 1, "backoff": 3.0, "min_batch": 10, "max_batch": 100}),
                    (("--max-retries", "5"), {"timeout": 180, "max_retries": 5, "backoff": 3.0, "min_batch": 10, "max_batch": 100}),
                    (("--request-timeout", "300"), {"timeout": 300, "max_retries": 1, "backoff": 3.0, "min_batch": 10, "max_batch": 100}),
                    (("--retry-backoff", "0"), {"timeout": 180, "max_retries": 1, "backoff": 0.0, "min_batch": 10, "max_batch": 100}),
                    (("--retry-backoff", "1.5"), {"timeout": 180, "max_retries": 1, "backoff": 1.5, "min_batch": 10, "max_batch": 100}),
                    (("--min-batch-size", "20", "--max-batch-size", "50"), {"timeout": 180, "max_retries": 1, "backoff": 3.0, "min_batch": 20, "max_batch": 50}),
                ]:
                    with mock.patch.dict(sys.modules, {"PySubtrans": fake_pysubtrans, "PySubtrans.Providers.Clients.CustomClient": mock.MagicMock()}), mock.patch.object(sys, "argv", argv + list(config_args)):
                        self.assertEqual(llm_subtrans_translate.main(), 0)

                    self.assertEqual(fake_pysubtrans.init_options.call_args.kwargs, {
                        "provider": "Custom Server",
                        "server_address": base_url.rstrip("/"),
                        "endpoint": "/chat/completions",
                        "supports_conversation": True,
                        "supports_system_messages": True,
                        "stream_responses": False,
                        "timeout": expected_kwargs["timeout"],
                        "max_retries": expected_kwargs["max_retries"],
                        "backoff_time": expected_kwargs["backoff"],
                        "min_batch_size": expected_kwargs["min_batch"],
                        "max_batch_size": expected_kwargs["max_batch"],
                        "api_key": "test-key",
                        "model": "grok-4.5",
                        "target_language": "Simplified Chinese",
                        "instruction_args": ["Use natural spoken Chinese."],
                    })


class BatchSizeArgumentsTest(unittest.TestCase):
    def test_batch_sizes_default_to_10_and_100_and_accept_positive_integers(self):
        argv = [str(SCRIPT), "--input", "input.srt", "--output", "output.srt", "--target", "Chinese",
                "--api-key", "offline-key", "--base-url", "http://example.test", "--model", "test-model"]
        for extra, expected_min, expected_max in [
            ([], 10, 100),
            (["--min-batch-size=20", "--max-batch-size=50"], 20, 50),
            (["--min-batch-size=1", "--max-batch-size=1"], 1, 1),
        ]:
            with self.subTest(extra=extra), mock.patch.object(sys, "argv", argv + extra):
                args = llm_subtrans_translate.parse_args()
                self.assertEqual(args.min_batch_size, expected_min)
                self.assertEqual(args.max_batch_size, expected_max)

    def test_rejects_invalid_batch_sizes_and_max_smaller_than_min(self):
        argv = [str(SCRIPT), "--input", "input.srt", "--output", "output.srt", "--target", "Chinese",
                "--api-key", "offline-key", "--base-url", "http://example.test", "--model", "test-model"]
        for extra in [
            ["--min-batch-size=0"],
            ["--min-batch-size=-1"],
            ["--min-batch-size=1.5"],
            ["--max-batch-size=0"],
            ["--max-batch-size=-5"],
            ["--max-batch-size=10.5"],
            ["--min-batch-size=50", "--max-batch-size=20"],
        ]:
            with self.subTest(extra=extra), mock.patch.object(sys, "argv", argv + extra), mock.patch("sys.stderr", io.StringIO()):
                with self.assertRaises(SystemExit) as raised:
                    llm_subtrans_translate.parse_args()
                self.assertEqual(raised.exception.code, 2)


class RetryBackoffArgumentsTest(unittest.TestCase):
    def test_retry_backoff_defaults_to_3_and_accepts_non_negative_numbers(self):
        argv = [str(SCRIPT), "--input", "input.srt", "--output", "output.srt", "--target", "Chinese",
                "--api-key", "offline-key", "--base-url", "http://example.test", "--model", "test-model"]
        for value, expected in [(None, 3.0), ("0", 0.0), ("1.5", 1.5), ("3", 3.0)]:
            with self.subTest(value=value), mock.patch.object(sys, "argv", argv + (["--retry-backoff=" + value] if value else [])):
                self.assertEqual(llm_subtrans_translate.parse_args().retry_backoff, expected)

    def test_rejects_invalid_backoff_before_initializing_translation(self):
        argv = [str(SCRIPT), "--input", "input.srt", "--output", "output.srt", "--target", "Chinese",
                "--api-key", "offline-key", "--base-url", "http://example.test", "--model", "test-model"]
        for value in ["-1", "-0.1", "NaN", "Inf", "invalid"]:
            with self.subTest(value=value), mock.patch.object(sys, "argv", argv + ["--retry-backoff=" + value]), mock.patch("sys.stderr", io.StringIO()):
                with self.assertRaises(SystemExit) as raised:
                    llm_subtrans_translate.parse_args()
                self.assertEqual(raised.exception.code, 2)


class RequestTimeoutArgumentsTest(unittest.TestCase):
    def test_request_timeout_defaults_to_180_and_accepts_positive_integers(self):
        argv = [str(SCRIPT), "--input", "input.srt", "--output", "output.srt", "--target", "Chinese",
                "--api-key", "offline-key", "--base-url", "http://example.test", "--model", "test-model"]
        for value, expected in [(None, 180), ("300", 300), ("1", 1)]:
            with self.subTest(value=value), mock.patch.object(sys, "argv", argv + (["--request-timeout=" + value] if value else [])):
                self.assertEqual(llm_subtrans_translate.parse_args().request_timeout, expected)

    def test_rejects_invalid_timeout_before_initializing_translation(self):
        argv = [str(SCRIPT), "--input", "input.srt", "--output", "output.srt", "--target", "Chinese",
                "--api-key", "offline-key", "--base-url", "http://example.test", "--model", "test-model"]
        for value in ["0", "-1", "1.5", "invalid"]:
            with self.subTest(value=value), mock.patch.object(sys, "argv", argv + ["--request-timeout=" + value]), mock.patch("sys.stderr", io.StringIO()):
                with self.assertRaises(SystemExit) as raised:
                    llm_subtrans_translate.parse_args()
                self.assertEqual(raised.exception.code, 2)


class MaxRetriesArgumentsTest(unittest.TestCase):
    def test_rejects_invalid_retry_count_before_initializing_translation(self):
        argv = [str(SCRIPT), "--input", "input.srt", "--output", "output.srt", "--target", "Chinese",
                "--api-key", "offline-key", "--base-url", "http://example.test", "--model", "test-model"]
        for value in ["0", "-1", "1.5", "invalid"]:
            with self.subTest(value=value), mock.patch.object(sys, "argv", argv + ["--max-retries=" + value]), mock.patch("sys.stderr", io.StringIO()):
                with self.assertRaises(SystemExit) as raised:
                    llm_subtrans_translate.parse_args()
                self.assertEqual(raised.exception.code, 2)


class RateLimitArgumentsTest(unittest.TestCase):
    def test_rate_limit_defaults_to_disabled_and_accepts_fractional_rpm(self):
        argv = [str(SCRIPT), "--input", "input.srt", "--output", "output.srt", "--target", "Chinese",
                "--api-key", "offline-key", "--base-url", "http://example.test", "--model", "test-model"]
        for value, expected in [(None, 0.0), ("80", 80.0), ("0.5", 0.5), ("0", 0.0)]:
            with self.subTest(value=value), mock.patch.object(sys, "argv", argv + (["--ratelimit=" + value] if value else [])):
                self.assertEqual(llm_subtrans_translate.parse_args().ratelimit, expected)

    def test_rejects_invalid_rate_before_initializing_translation(self):
        argv = [str(SCRIPT), "--input", "input.srt", "--output", "output.srt", "--target", "Chinese",
                "--api-key", "offline-key", "--base-url", "http://example.test", "--model", "test-model"]
        for value in ["-1", "nan", "inf", "-inf", "invalid"]:
            with self.subTest(value=value), mock.patch.object(sys, "argv", argv + ["--ratelimit=" + value]), mock.patch("sys.stderr", io.StringIO()):
                with self.assertRaises(SystemExit) as raised:
                    llm_subtrans_translate.parse_args()
                self.assertEqual(raised.exception.code, 2)


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
        fake_pysubtrans.init_translation_provider = mock.Mock()
        fake_pysubtrans.init_translator = lambda options, translation_provider=None: FakeTranslator()

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

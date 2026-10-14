"""Offline protocol tests; run in the image to use its installed PySubtrans."""

import importlib.util
import json
import pathlib
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest import mock


SCRIPT = pathlib.Path(__file__).with_name("llm_subtrans_translate.py")
spec = importlib.util.spec_from_file_location("llm_subtrans_translate_integration", SCRIPT)
llm_subtrans_translate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(llm_subtrans_translate)


@unittest.skipUnless(importlib.util.find_spec("PySubtrans"), "PySubtrans is installed in the Docker image")
class CustomServerProtocolTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.input = pathlib.Path(tmp.name, "input.srt")
        self.output = pathlib.Path(tmp.name, "output.srt")
        self.input.write_text("1\n00:00:01,000 --> 00:00:02,000\nこんにちは。\n", encoding="utf-8")
        self.requests = []
        self.status = 200
        self.status_sequence = None
        self.response_line_numbers = None
        test = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                test.requests.append((self.path, self.headers.get("Authorization"), body))
                status = test.status_sequence[len(test.requests) - 1] if test.status_sequence else test.status
                if status == 200:
                    line_number = test.response_line_numbers[len(test.requests) - 1] if test.response_line_numbers else 1
                    content = {
                        "model": "grok-4.5",
                        "choices": [{
                            "finish_reason": "stop",
                            "message": {"content": f"#{line_number}\nOriginal>\nこんにちは。\nTranslation>\n你好。\n<summary>A greeting.</summary>\n<scene>A greeting.</scene>"},
                        }],
                    }
                else:
                    content = {"error": {"message": "service unavailable"}}
                data = json.dumps(content).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *_args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.base_url = f"http://127.0.0.1:{server.server_port}"
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(thread.join)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)

    def run_translation(self, base_url, *extra_args):
        argv = [
            str(SCRIPT),
            "--input", str(self.input),
            "--output", str(self.output),
            "--target", "Simplified Chinese",
            "--api-key", "test-key",
            "--base-url", base_url,
            "--model", "grok-4.5",
            "--instruction", "Use natural spoken Chinese.",
        ]
        argv.extend(extra_args)
        with mock.patch.object(sys, "argv", argv):
            return llm_subtrans_translate.main()

    def test_rate_limit_spaces_successful_translation_requests(self):
        # Separate scenes force two real Custom Server requests. Only the
        # translation client's clock is replaced; HTTP and parsing stay real.
        self.input.write_text(
            "1\n00:00:01,000 --> 00:00:02,000\nこんにちは。\n\n"
            "2\n00:02:01,000 --> 00:02:02,000\nこんにちは。\n",
            encoding="utf-8",
        )
        self.response_line_numbers = [1, 2]
        for args, times, expected in [
            (("--ratelimit", "80"), [0, 0, 1, 1], [0.75, 0.75]),
            (("--ratelimit", "80"), [0, 2, 3, 5], []),
            ((), [0, 0, 1, 1], []),
            (("--ratelimit", "0"), [0, 0, 1, 1], []),
        ]:
            with self.subTest(args=args, times=times):
                self.requests.clear()
                self.output.unlink(missing_ok=True)
                sleeps = []
                with mock.patch("PySubtrans.TranslationClient.time") as clock:
                    clock.monotonic.side_effect = times
                    clock.sleep.side_effect = sleeps.append
                    self.assertEqual(self.run_translation(self.base_url + "/v1", *args), 0)
                self.assertEqual(len(self.requests), 2)
                self.assertEqual(sleeps, expected)
                self.assertTrue(self.output.exists())

    def test_translates_through_chat_endpoint_preserving_base_path(self):
        for prefix in ["/v1", "/v1/", "/proxy/v1"]:
            with self.subTest(prefix=prefix):
                self.requests.clear()
                self.output.unlink(missing_ok=True)
                self.assertEqual(self.run_translation(self.base_url + prefix), 0)
                translated = self.output.read_text(encoding="utf-8")
                self.assertIn("你好。", translated)
                self.assertIn("00:00:01,000 --> 00:00:02,000", translated)
                self.assertEqual(len(self.requests), 1)
                path, authorization, body = self.requests[0]
                self.assertEqual(path, prefix.rstrip("/") + "/chat/completions")
                self.assertEqual(authorization, "Bearer test-key")
                self.assertEqual(body["model"], "grok-4.5")
                self.assertFalse(body["stream"])
                self.assertNotIn("prompt", body)
                self.assertEqual([m["role"] for m in body["messages"]], ["system", "user"])
                self.assertIn("Use natural spoken Chinese.", body["messages"][0]["content"])

    def test_server_failure_stops_after_configured_retries(self):
        from PySubtrans.SubtitleError import TranslationImpossibleError

        self.status = 503
        for args, retries in [((), 1), (("--max-retries", "1"), 1), (("--max-retries", "5"), 5)]:
            with self.subTest(args=args):
                self.requests.clear()
                with mock.patch("time.sleep") as sleep, self.assertRaises(TranslationImpossibleError):
                    self.run_translation(self.base_url + "/v1", *args)
                self.assertEqual(len(self.requests), retries + 1)
                self.assertEqual(sleep.call_args_list, [mock.call(3.0 * 2 ** i) for i in range(retries)])
                self.assertFalse(self.output.exists())

    def test_retry_recovers_batch_without_repeating_completed_batch(self):
        self.input.write_text(
            "1\n00:00:01,000 --> 00:00:02,000\nこんにちは。\n\n"
            "2\n00:02:01,000 --> 00:02:02,000\nこんにちは。\n\n"
            "3\n00:04:01,000 --> 00:04:02,000\nこんにちは。\n",
            encoding="utf-8",
        )
        self.status_sequence = [200, 503, 503, 200, 200]
        self.response_line_numbers = [1, 2, 2, 2, 3]
        with mock.patch("time.sleep") as sleep:
            self.assertEqual(self.run_translation(self.base_url + "/v1", "--max-retries", "5"), 0)
        self.assertEqual(len(self.requests), 5)
        self.assertEqual([self.requests[i][2] for i in (1, 2)], [self.requests[3][2]] * 2)
        self.assertEqual(sleep.call_args_list, [mock.call(3.0), mock.call(6.0)])
        translated = self.output.read_text(encoding="utf-8")
        self.assertEqual(translated.count("你好。"), 3)
        for timestamp in ("00:00:01,000", "00:02:01,000", "00:04:01,000"):
            self.assertIn(timestamp, translated)

    def test_read_timeout_uses_configured_seconds_and_retries(self):
        import httpx
        from PySubtrans.SubtitleError import TranslationImpossibleError

        requests = []
        original_client = httpx.Client

        def timeout(request):
            requests.append(request)
            raise httpx.ReadTimeout("injected offline timeout", request=request)

        def create_client(**kwargs):
            return original_client(**kwargs, transport=httpx.MockTransport(timeout))

        for args, retries, expected_timeout in [
            ((), 1, 180),
            (("--max-retries", "5"), 5, 180),
            (("--request-timeout", "300"), 1, 300),
            (("--max-retries", "2", "--request-timeout", "240"), 2, 240),
        ]:
            with self.subTest(args=args):
                requests.clear()
                with mock.patch.object(httpx, "Client", side_effect=create_client), mock.patch("time.sleep"):
                    with self.assertRaises(TranslationImpossibleError):
                        self.run_translation(self.base_url + "/v1", *args)
                self.assertEqual(len(requests), retries + 1)
                self.assertTrue(all(request.extensions["timeout"]["read"] == expected_timeout for request in requests))
                self.assertFalse(self.output.exists())


if __name__ == "__main__":
    unittest.main()

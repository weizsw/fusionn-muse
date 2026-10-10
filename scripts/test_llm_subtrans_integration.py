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
        test = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                test.requests.append((self.path, self.headers.get("Authorization"), body))
                if test.status == 200:
                    content = {
                        "model": "grok-4.5",
                        "choices": [{
                            "finish_reason": "stop",
                            "message": {"content": "#1\nOriginal>\nこんにちは。\nTranslation>\n你好。\n<summary>A greeting.</summary>\n<scene>A greeting.</scene>"},
                        }],
                    }
                else:
                    content = {"error": {"message": "service unavailable"}}
                data = json.dumps(content).encode()
                self.send_response(test.status)
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

    def run_translation(self, base_url):
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
        with mock.patch.object(sys, "argv", argv):
            return llm_subtrans_translate.main()

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

    def test_server_failure_stops_after_one_retry(self):
        from PySubtrans.SubtitleError import TranslationImpossibleError

        self.status = 503
        with mock.patch("time.sleep"), self.assertRaises(TranslationImpossibleError):
            self.run_translation(self.base_url + "/v1")
        self.assertEqual(len(self.requests), 2)
        self.assertFalse(self.output.exists())

    def test_read_timeout_uses_180_seconds_and_only_one_retry(self):
        import httpx
        from PySubtrans.SubtitleError import TranslationImpossibleError

        requests = []
        original_client = httpx.Client

        def timeout(request):
            requests.append(request)
            raise httpx.ReadTimeout("injected offline timeout", request=request)

        def create_client(**kwargs):
            return original_client(**kwargs, transport=httpx.MockTransport(timeout))

        with mock.patch.object(httpx, "Client", side_effect=create_client), mock.patch("time.sleep"):
            with self.assertRaises(TranslationImpossibleError):
                self.run_translation(self.base_url + "/v1")
        self.assertEqual(len(requests), 2)
        self.assertEqual(requests[0].extensions["timeout"]["read"], 180)
        self.assertEqual(requests[1].extensions["timeout"]["read"], 180)
        self.assertFalse(self.output.exists())


if __name__ == "__main__":
    unittest.main()

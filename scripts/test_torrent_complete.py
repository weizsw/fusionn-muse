import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


SCRIPT = Path(__file__).with_name("torrent-complete.sh")


@unittest.skipUnless(shutil.which("curl"), "curl unavailable")
class TorrentCompleteTests(unittest.TestCase):
    def invoke(self, url, directory, values):
        log = Path(directory) / "hook.log"
        result = subprocess.run(
            ["bash", str(SCRIPT), *values],
            env={**os.environ, "MUSE_URL": url, "LOG_FILE": str(log), "HOST_INPUT_PATH": ""},
            capture_output=True, text=True, timeout=20,
        )
        return result, log.read_text()

    def serve(self, status, requests):
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = self.rfile.read(int(self.headers["Content-Length"]))
                requests.append((self.path, json.loads(body)))
                self.send_response(status)
                self.end_headers()
                self.wfile.write(b'{"message":"result"}')

            def log_message(self, *_):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(lambda: thread.join(timeout=1))
        self.addCleanup(server.shutdown)
        return f"http://127.0.0.1:{server.server_port}"

    def test_valid_json_preserves_quotes_unicode_and_control_characters(self):
        requests = []
        url = self.serve(202, requests)
        values = ['/downloads/"quote"\\name\n\t\r\x01\x1f中文.mp4', '种子 "name"\\', 'video\ncategory']
        with tempfile.TemporaryDirectory() as directory:
            result, log = self.invoke(url, directory, values)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(requests, [("/api/v1/webhook/torrent", dict(zip(("path", "name", "category"), values)))])
        self.assertIn("sent successfully (HTTP 202)", log)

    def test_http_failure_redirect_and_unexpected_success_are_not_acknowledged(self):
        for status in (503, 302, 200):
            with self.subTest(status=status):
                requests = []
                url = self.serve(status, requests)
                with tempfile.TemporaryDirectory() as directory:
                    result, log = self.invoke(url, directory, ["/movie.mp4", "movie", "video"])
                self.assertEqual(result.returncode, 1)
                self.assertEqual(len(requests), 1, "must not silently retry")
                self.assertIn(f"HTTP {status}", log)
                self.assertNotIn("sent successfully", log)

    def test_unreachable_muse_is_logged_and_returned_as_failure(self):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        with tempfile.TemporaryDirectory() as directory:
            result, log = self.invoke(f"http://127.0.0.1:{port}", directory, ["/movie.mp4", "movie", "video"])
        self.assertEqual(result.returncode, 1)
        self.assertIn("Webhook failed", log)
        self.assertNotIn("sent successfully", log)


if __name__ == "__main__":
    unittest.main()

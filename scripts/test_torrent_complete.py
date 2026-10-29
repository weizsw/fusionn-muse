import json
import os
from pathlib import Path
import re
import socket
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


import torrent_complete

SCRIPT = Path(__file__).with_name("torrent_complete.py")

# The shared request ID rule: ASCII letters, digits, dot, underscore, hyphen; 1-64.
REQUEST_ID_RE = re.compile(r"^[A-Za-z0-9._-]{1,64}$")

# Correlation fields render right after the event, in this fixed order.
CORRELATION_KEYS = ["job_id", "submission_id", "attempt_id", "request_id", "parent_job_id", "stage", "tool", "stream"]

# component and event are fixed in that order; msg is last; the line is one physical line.
LINE_RE = re.compile(
    r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}\+08:00 "
    r"(DEBUG|INFO|WARN|ERROR) component=torrent-complete event=\S+ "
    r'.*msg=".*"$'
)

ACK = b'{"message":"webhook accepted","status":"pending","submission_id":"sub-123"}'
MOUNTED = "/Volumes/External/downloads/movie.mp4"


class TorrentCompleteTests(unittest.TestCase):
    """The hook run as qBittorrent runs it, against a local stand-in for fusionn-muse."""

    def serve(self, status, reply=ACK):
        requests = []

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = self.rfile.read(int(self.headers["Content-Length"]))
                requests.append(
                    {"path": self.path, "body": json.loads(body), "request_id": self.headers.get("X-Request-ID")}
                )
                self.send_response(status)
                if 300 <= status < 400:
                    self.send_header("Location", "/elsewhere")
                self.send_header("Content-Length", str(len(reply)))
                self.end_headers()
                self.wfile.write(reply)

            def log_message(self, *_):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(lambda: thread.join(timeout=1))
        self.addCleanup(server.shutdown)
        return "http://127.0.0.1:%d" % server.server_port, requests

    def invoke(self, url, values, python=sys.executable, level=None, log_file=None):
        with tempfile.TemporaryDirectory() as directory:
            log = Path(log_file) if log_file else Path(directory) / "hook.log"
            environment = {**os.environ, "MUSE_URL": url, "LOG_FILE": str(log)}
            for name in ("LOG_LEVEL", "ALIGN_LOG_LEVEL"):
                environment.pop(name, None)
            if level is not None:
                environment["LOG_LEVEL"] = level
            result = subprocess.run(
                [python, str(SCRIPT), *values], env=environment, capture_output=True, text=True, timeout=30
            )
            text = log.read_text() if log.is_file() else ""
        return result, text

    def assert_unified_lines(self, text):
        lines = text.strip().split("\n")
        self.assertTrue(lines[0], "no log lines")
        for line in lines:
            self.assertRegex(line, LINE_RE)
        return lines

    def test_whisparr_is_sent_with_the_mapped_path_and_one_request_id(self):
        url, requests = self.serve(202)
        name = '种子 "name"\\\n'
        path = '/Volumes/External/downloads/"quote"\\name\n\t\x01中文.mp4'
        result, log = self.invoke(url, [name, "whisparr", "abc123", path])

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "", "logs must not pollute stdout")
        self.assertEqual(len(requests), 1)
        self.assertEqual(requests[0]["path"], "/api/v1/webhook/torrent")
        self.assertEqual(
            requests[0]["body"],
            {"path": '/data/downloads/"quote"\\name\n\t\x01中文.mp4', "name": name, "category": "whisparr"},
        )
        request_id = requests[0]["request_id"] or ""
        self.assertRegex(request_id, REQUEST_ID_RE)

        lines = self.assert_unified_lines(log)
        for line in lines:
            self.assertIn(" request_id=%s " % request_id, line, "every line of the run carries the sent ID")
        sent = [line for line in lines if "event=webhook_sent" in line]
        self.assertEqual(len(sent), 1)
        self.assertIn("event=webhook_sent submission_id=sub-123 request_id=%s " % request_id, sent[0])
        self.assertIn("info_hash=abc123", log)
        # Every value stays on one physical line, with the shared escape rules.
        for escaped in (r"\n", r"\t", r"\x01", r"\"", "中文"):
            self.assertIn(escaped, log)
        self.assertEqual(log, result.stderr, "LOG_FILE gets the same lines as stderr")

    def test_other_categories_are_only_logged(self):
        url, requests = self.serve(202)
        result, log = self.invoke(url, ["movie", "radarr", "abc123", MOUNTED])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(requests, [])
        self.assert_unified_lines(log)
        self.assertIn("event=torrent_skipped", log)
        self.assertNotIn("event=webhook_sent", log)

    def test_a_path_outside_the_mount_fails_without_a_request(self):
        url, requests = self.serve(202)
        for path in ("/Volumes/ExternalOther/movie.mp4", "/Users/me/movie.mp4", "downloads/movie.mp4"):
            with self.subTest(path=path):
                result, log = self.invoke(url, ["movie", "whisparr", "abc123", path])
                self.assertEqual(result.returncode, 1)
                self.assert_unified_lines(log)
                self.assertIn("ERROR component=torrent-complete event=webhook_failed", log)
                self.assertIn('error="path is outside the download mount"', log)
        self.assertEqual(requests, [])

    def test_http_failure_redirect_and_unexpected_success_are_not_acknowledged(self):
        for status in (503, 302, 200):
            with self.subTest(status=status):
                url, requests = self.serve(status)
                result, log = self.invoke(url, ["movie", "whisparr", "abc123", MOUNTED])
                self.assertEqual(result.returncode, 1)
                self.assertEqual(len(requests), 1, "one request; a redirect is not followed")
                self.assert_unified_lines(log)
                self.assertIn("ERROR component=torrent-complete event=webhook_failed", log)
                self.assertIn("http_status=%d" % status, log)
                self.assertNotIn("event=webhook_sent", log)

    def test_an_acknowledgement_without_a_valid_submission_id_fails(self):
        replies = (
            b'{"status":"pending"}',
            b"not json BODY_SENTINEL",
            b'{"submission_id":"bad id\\nBODY_SENTINEL"}',
            b"[]",
        )
        for reply in replies:
            with self.subTest(reply=reply):
                url, requests = self.serve(202, reply)
                result, log = self.invoke(url, ["movie", "whisparr", "abc123", MOUNTED])
                self.assertEqual(result.returncode, 1)
                self.assertEqual(len(requests), 1)
                self.assert_unified_lines(log)
                self.assertIn('error="missing or invalid submission_id"', log)
                self.assertNotIn("BODY_SENTINEL", log, "the response body is never logged")
                self.assertNotIn("submission_id=", log)
                self.assertNotIn("event=webhook_sent", log)

    def test_an_unreachable_muse_is_a_failure(self):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        result, log = self.invoke("http://127.0.0.1:%d" % port, ["movie", "whisparr", "abc123", MOUNTED])
        self.assertEqual(result.returncode, 1)
        self.assert_unified_lines(log)
        self.assertIn('error="request transport failure"', log)
        self.assertIn("error_type=", log)
        self.assertNotIn("event=webhook_sent", log)

    def test_missing_arguments_fail(self):
        result, log = self.invoke("http://127.0.0.1:9", ["movie", "whisparr", "abc123"])
        self.assertEqual(result.returncode, 1)
        self.assert_unified_lines(log)
        self.assertIn("ERROR component=torrent-complete event=arguments_invalid", log)
        self.assertIn("arguments=3", log)

    def test_fields_are_ordered_and_msg_is_last(self):
        url, _ = self.serve(202)
        _, log = self.invoke(url, ["movie", "whisparr", "abc123", MOUNTED])
        for line in self.assert_unified_lines(log):
            keys = re.findall(r"(?:^| )([a-z_]+)=", line)
            self.assertEqual(keys[:2], ["component", "event"])
            self.assertEqual(keys[-1], "msg")
            rest = keys[2:-1]
            correlation = [key for key in rest if key in CORRELATION_KEYS]
            self.assertIn("request_id", correlation)
            self.assertEqual(rest[: len(correlation)], correlation, "correlation fields come first")
            self.assertEqual(correlation, [key for key in CORRELATION_KEYS if key in correlation], "in the fixed order")
            self.assertEqual(rest[len(correlation) :], sorted(rest[len(correlation) :]))

    def test_log_level(self):
        url, _ = self.serve(202)
        _, quiet = self.invoke(url, ["movie", "whisparr", "abc123", MOUNTED], level="ERROR")
        self.assertEqual(quiet, "", "ERROR hides a successful run")

        _, warned = self.invoke(url, ["movie", "whisparr", "abc123", "/elsewhere/movie.mp4"], level="warning")
        self.assertIn("event=webhook_failed", warned)
        self.assertNotIn("event=torrent_completed", warned, "WARNING is an alias of WARN")

        _, fallback = self.invoke(url, ["movie", "whisparr", "abc123", MOUNTED], level="LOUD")
        self.assertIn("WARN component=torrent-complete event=config_invalid", fallback)
        self.assertIn("log_level=LOUD", fallback)
        self.assertIn("event=webhook_sent", fallback, "an invalid level falls back to INFO")

    def test_an_unwritable_log_file_does_not_fail_the_run(self):
        url, requests = self.serve(202)
        with tempfile.TemporaryDirectory() as directory:
            # A directory cannot be opened for appending.
            result, _ = self.invoke(url, ["movie", "whisparr", "abc123", MOUNTED], log_file=directory)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(requests), 1)
        self.assertIn("event=webhook_sent", result.stderr)

    def test_a_closed_stderr_does_not_stop_the_webhook(self):
        """qBittorrent can start the hook with stderr closed; the request is still sent."""
        url, requests = self.serve(202)
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / "hook.log"
            environment = {**os.environ, "MUSE_URL": url, "LOG_FILE": str(log)}
            for name in ("LOG_LEVEL", "ALIGN_LOG_LEVEL"):
                environment.pop(name, None)
            # "2>&-" closes file descriptor 2 before Python starts.
            command = ["/bin/sh", "-c", 'exec "$0" "$@" 2>&-', sys.executable, str(SCRIPT), "movie", "whisparr", "abc123", MOUNTED]
            result = subprocess.run(command, env=environment, capture_output=True, text=True, timeout=30)
            text = log.read_text() if log.is_file() else ""
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertEqual(len(requests), 1)
        self.assertIn("event=webhook_sent", text)

    @unittest.skipUnless(Path("/usr/bin/python3").exists(), "/usr/bin/python3 unavailable")
    def test_runs_on_the_system_python(self):
        """The qBittorrent host may have only the system Python (3.9 on macOS)."""
        url, requests = self.serve(202)
        result, log = self.invoke(url, ["movie", "whisparr", "abc123", MOUNTED], python="/usr/bin/python3")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(requests), 1)
        self.assertIn("event=webhook_sent", log)


class HookRenderingTest(unittest.TestCase):
    """The hook renders like the shared renderer without importing it."""

    hook = torrent_complete

    def test_values_match_the_shared_renderer(self):
        import logfmt
        from test_logfmt import FIXED_MOMENT, SHARED_VALUE_MATRIX

        for value, _ in SHARED_VALUE_MATRIX:
            if isinstance(value, str):
                with self.subTest(value=value):
                    self.assertEqual(self.hook.format_value(value), logfmt.format_value(value))
        self.assertEqual(self.hook.format_timestamp(FIXED_MOMENT), logfmt.format_timestamp(FIXED_MOMENT))

    def test_no_value_can_forge_a_line(self):
        line = self.hook.format_line(
            self.hook.INFO,
            "torrent_completed",
            "Torrent completed",
            {"name": "line\nnext", "path": "ev\nil=movie", "error": "a\r\nb"},
        )
        self.assertNotIn("\n", line)
        self.assertNotIn("\r", line)
        self.assertIn('error="a\\r\\nb"', line)

    def test_path_mapping(self):
        cases = {
            "/Volumes/External": "/data",
            "/Volumes/External/a b/c.mkv": "/data/a b/c.mkv",
            "/Volumes/ExternalOther/c.mkv": None,
            "/Users/me/c.mkv": None,
        }
        for path, want in cases.items():
            with self.subTest(path=path):
                self.assertEqual(self.hook.container_path(path), want)


if __name__ == "__main__":
    unittest.main()

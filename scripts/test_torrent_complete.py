import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


SCRIPT = Path(__file__).with_name("torrent-complete.sh")

# The shared request ID rule: ASCII letters, digits, dot, underscore, hyphen; 1-64.
REQUEST_ID_RE = re.compile(r"^[A-Za-z0-9._-]{1,64}$")

# Correlation fields render right after the event, in this fixed order.
CORRELATION_KEYS = ["job_id", "submission_id", "attempt_id", "request_id", "parent_job_id"]

# component and event are fixed in that order; msg is last; the line is one physical line.
LINE_RE = re.compile(
    r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}\+08:00 "
    r"(DEBUG|INFO|WARN|ERROR) component=torrent-complete event=\S+ "
    r'.*msg=".*"$'
)


def load_log_value(shell):
    """Extract the value renderer from the script and return a runner for it."""
    source = SCRIPT.read_text()
    start = source.index("utf8_codepoint() {")
    end = source.index("\n}\n", source.index("log_value() {")) + 3
    runner = source[start:end] + '\nfor value in "$@"; do printf \'%s\\n\' "$(log_value "$value")"; done\n'

    def run(values):
        result = subprocess.run(
            [shell, "-c", runner, "shell", *values], capture_output=True, text=True, timeout=20
        )
        if result.returncode != 0:
            raise AssertionError(result.stderr)
        return result.stdout.splitlines()

    return run


@unittest.skipUnless(shutil.which("curl"), "curl unavailable")
class TorrentCompleteTests(unittest.TestCase):
    def invoke(self, url, directory, values, shell="bash"):
        log = Path(directory) / "hook.log"
        result = subprocess.run(
            [shell, str(SCRIPT), *values],
            env={**os.environ, "MUSE_URL": url, "LOG_FILE": str(log), "HOST_INPUT_PATH": ""},
            capture_output=True, text=True, timeout=20,
        )
        return result, log.read_text()

    def assert_unified_lines(self, text):
        lines = text.strip().split("\n")
        self.assertTrue(lines)
        for line in lines:
            self.assertRegex(line, LINE_RE)

    def serve(self, status, requests, request_ids=None, reply=b'{"message":"result"}'):
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = self.rfile.read(int(self.headers["Content-Length"]))
                requests.append((self.path, json.loads(body)))
                if request_ids is not None:
                    request_ids.append(self.headers.get("X-Request-ID"))
                self.send_response(status)
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
        return f"http://127.0.0.1:{server.server_port}"

    def test_valid_json_preserves_quotes_unicode_and_control_characters(self):
        requests = []
        url = self.serve(202, requests)
        values = ['/downloads/"quote"\\name\n\t\r\x01\x1f中文.mp4', '种子 "name"\\', 'video\ncategory']
        with tempfile.TemporaryDirectory() as directory:
            result, log = self.invoke(url, directory, values)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(requests, [("/api/v1/webhook/torrent", dict(zip(("path", "name", "category"), values)))])
        self.assert_unified_lines(log)
        self.assertIn("event=torrent_completed", log)
        self.assertIn("event=webhook_sent", log)
        self.assertIn("http_status=202", log)
        # Every value stays on one physical line, with the shared escape rules.
        self.assertIn("中文", log)
        self.assertIn(r"\n", log)
        self.assertIn(r"\t", log)
        self.assertIn(r"\r", log)
        self.assertIn(r"\x01", log)
        self.assertIn(r"\x1f", log)
        self.assertIn(r'\"', log)
        self.assertEqual(result.stdout, "", "logs must not pollute stdout")

    def test_http_failure_redirect_and_unexpected_success_are_not_acknowledged(self):
        for status in (503, 302, 200):
            with self.subTest(status=status):
                requests = []
                url = self.serve(status, requests)
                with tempfile.TemporaryDirectory() as directory:
                    result, log = self.invoke(url, directory, ["/movie.mp4", "movie", "video"])
                self.assertEqual(result.returncode, 1)
                self.assertEqual(len(requests), 1, "must not silently retry")
                self.assert_unified_lines(log)
                self.assertIn("ERROR component=torrent-complete event=webhook_failed", log)
                self.assertIn(f"http_status={status}", log)
                self.assertNotIn("event=webhook_sent", log)

    def test_unreachable_muse_is_logged_and_returned_as_failure(self):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        with tempfile.TemporaryDirectory() as directory:
            result, log = self.invoke(f"http://127.0.0.1:{port}", directory, ["/movie.mp4", "movie", "video"])
        self.assertEqual(result.returncode, 1)
        self.assertIn("event=webhook_failed", log)
        self.assertNotIn("event=webhook_sent", log)
        self.assert_unified_lines(log)
        self.assertFalse(re.search(r"curl_status=0\b", log), "a failed curl must not be reported as success")

    def test_fields_are_sorted_and_msg_is_last(self):
        requests = []
        url = self.serve(202, requests, reply=b'{"status":"pending","submission_id":"sub-1"}')
        with tempfile.TemporaryDirectory() as directory:
            _, log = self.invoke(url, directory, ["/movie.mp4", "movie", "video"])
        for line in log.strip().split("\n"):
            keys = re.findall(r"(?:^| )([a-z_]+)=", line)
            self.assertEqual(keys[:2], ["component", "event"])
            self.assertEqual(keys[-1], "msg")
            rest = keys[2:-1]
            correlation = [key for key in rest if key in CORRELATION_KEYS]
            self.assertIn("request_id", correlation)
            self.assertEqual(rest[: len(correlation)], correlation, "correlation fields come first")
            self.assertEqual(correlation, [key for key in CORRELATION_KEYS if key in correlation], "in the fixed order")
            self.assertEqual(rest[len(correlation):], sorted(rest[len(correlation):]))

    def test_request_id_links_the_hook_to_the_submission(self):
        requests, request_ids = [], []
        url = self.serve(202, requests, request_ids, reply=b'{"message":"webhook accepted","status":"pending","submission_id":"sub-123"}')
        with tempfile.TemporaryDirectory() as directory:
            result, log = self.invoke(url, directory, ["/movie.mp4", "movie", "video"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(request_ids), 1)
        request_id = request_ids[0]
        self.assertRegex(request_id or "", REQUEST_ID_RE)
        lines = log.strip().split("\n")
        for line in lines:
            self.assertIn(f" request_id={request_id} ", line, "every line of the run carries the sent ID")
        sent = [line for line in lines if "event=webhook_sent" in line]
        self.assertEqual(len(sent), 1)
        self.assertIn(f"event=webhook_sent submission_id=sub-123 request_id={request_id} ", sent[0])

    def test_unsafe_submission_id_is_not_logged(self):
        requests = []
        url = self.serve(202, requests, reply=b'{"submission_id":"bad id\\nnext"}')
        with tempfile.TemporaryDirectory() as directory:
            result, log = self.invoke(url, directory, ["/movie.mp4", "movie", "video"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_unified_lines(log)
        self.assertNotIn("submission_id=", log)
        self.assertNotIn("bad id", log)

    @unittest.skipUnless(Path("/bin/bash").exists(), "/bin/bash unavailable")
    def test_runs_on_the_system_bash(self):
        """macOS ships bash 3.2 at /bin/bash; the script must not need bash 4+."""
        requests = []
        url = self.serve(202, requests)
        with tempfile.TemporaryDirectory() as directory:
            result, log = self.invoke(url, directory, ["/movie.mp4", "movie", "video"], shell="/bin/bash")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_unified_lines(log)
        self.assertIn("event=webhook_sent", log)


def load_logger(shell):
    """Extract the whole logging section so log_line can be driven directly."""
    source = SCRIPT.read_text()
    start = source.index("utf8_codepoint() {")
    end = source.index("\n}\n", source.index("log_line() {")) + 3
    return source[start:end]


class ShellLevelTest(unittest.TestCase):
    """LOG_LEVEL and the message quoting rule, driven through the real functions."""

    def run_log(self, shell, lines, level=None):
        with tempfile.TemporaryDirectory() as directory:
            log_file = Path(directory, "out.log")
            body = "\n".join(lines)
            runner = '#!/bin/bash\nLOG_FILE="$LOG_FILE_PATH"\n%s\ndetect_time_precision\nresolve_log_level\n%s\n' % (
                load_logger(shell),
                body,
            )
            environment = {
                **os.environ,
                "LOG_FILE_PATH": str(log_file),
                "PYTHONDONTWRITEBYTECODE": "1",
            }
            if level is None:
                environment.pop("LOG_LEVEL", None)
            else:
                environment["LOG_LEVEL"] = level
            result = subprocess.run(
                [shell, "-c", runner], capture_output=True, text=True, timeout=20, env=environment
            )
            if result.returncode != 0:
                raise AssertionError(result.stderr)
            return log_file.read_text() if log_file.exists() else ""

    def shells(self):
        return [shell for shell in (shutil.which("bash"), "/bin/bash") if shell and Path(shell).exists()]

    def test_a_one_word_message_is_still_quoted(self):
        for shell in self.shells():
            with self.subTest(shell=shell):
                self.assertIn('msg="ready"', self.run_log(shell, ["log_line INFO test_event ready"]))

    def test_error_level_suppresses_a_successful_info_run(self):
        for shell in self.shells():
            with self.subTest(shell=shell):
                self.assertEqual(self.run_log(shell, ["log_line INFO test_event ready"], level="ERROR"), "")

    def test_error_level_still_reports_a_failure(self):
        for shell in self.shells():
            with self.subTest(shell=shell):
                self.assertIn("ERROR", self.run_log(shell, ["log_line ERROR test_event failed"], level="ERROR"))

    def test_correlation_ids_cannot_be_passed_as_fields(self):
        for shell in self.shells():
            with self.subTest(shell=shell):
                output = self.run_log(
                    shell,
                    [
                        'log_line INFO test_event ready "request_id=forged" "job_id=forged"',
                        'LOG_REQUEST_ID=real; log_line INFO test_event ready "request_id=forged"',
                    ],
                )
                self.assertNotIn("forged", output)
                self.assertEqual(output.count("request_id=real"), 1)

    def test_warning_is_a_compatibility_alias(self):
        for shell in self.shells():
            with self.subTest(shell=shell):
                self.assertIn("WARN", self.run_log(shell, ["log_line WARN test_event careful"], level="warning"))

    def test_an_invalid_level_is_reported_and_falls_back_to_info(self):
        for shell in self.shells():
            with self.subTest(shell=shell):
                output = self.run_log(
                    shell,
                    [
                        'if [[ -n "$INVALID_LEVEL" ]]; then log_line WARN config_invalid'
                        ' "Invalid LOG_LEVEL, using INFO" "log_level=$INVALID_LEVEL"; fi',
                        "log_line INFO test_event ready",
                    ],
                    level="LOUD",
                )
                self.assertIn("event=config_invalid", output)
                self.assertIn("log_level=LOUD", output)
                self.assertIn("event=test_event", output, "the fallback is INFO")


class ShellValueEscapingTest(unittest.TestCase):
    """The shell must render a value exactly like the Go and Python renderers."""

    def test_log_value_matches_the_shared_renderer(self):
        import logfmt
        from test_logfmt import SHARED_VALUE_MATRIX

        values = [value for value, _ in SHARED_VALUE_MATRIX if isinstance(value, str)]
        expected = [logfmt.format_value(value) for value in values]

        for shell in [shutil.which("bash"), "/bin/bash"]:
            if not shell or not Path(shell).exists():
                continue
            with self.subTest(shell=shell):
                self.assertEqual(load_log_value(shell)(values), expected)

    def test_no_value_can_forge_a_line(self):
        values = ["line\nnext", "ev\nil=movie", "name=\nbad", "\r\nreset"]
        for shell in [shutil.which("bash"), "/bin/bash"]:
            if not shell or not Path(shell).exists():
                continue
            with self.subTest(shell=shell):
                rendered = load_log_value(shell)(values)
                self.assertEqual(len(rendered), len(values))
                for line in rendered:
                    self.assertNotIn("\n", line[1:-1])
                    self.assertTrue(line.startswith('"') and line.endswith('"'))


if __name__ == "__main__":
    unittest.main()

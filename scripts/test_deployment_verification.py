"""Execute the delivered CLI against fault-injection HTTP fixtures.

These are simulated contract consumers, not deployment/model acceptance tests.
Real provider compatibility is tested in internal/handler/deployment_test.go.
"""

import copy
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import threading
import time
import unittest

ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "scripts/verify_deployment.py"
SRT = "1\n00:00:00,000 --> 00:00:01,000\n测试字幕\n"


def completed():
    return {
        "kind": "job", "id": "job-current", "status": "completed", "job_status": "completed",
        "stage": "delivered", "execution_id": 2, "execution_kind": "initial", "is_light": False,
        "job": {
            "id": "job-current", "created_at": "2026-01-01T00:00:00.000000002Z",
            "attempt_id": 2, "attempt_kind": "initial", "attempt_status": "completed",
            "status": "completed", "checkpoint": "delivered", "is_light": False,
            "processing_path": "/data/automation/scraping/动态 name.mkv",
            "subtitle_path": "/data/automation/transcriptions/动态 name.srt",
            "translated_path": "/data/automation/subtitles/动态 name.zh.srt",
            "transcription_source": "mlx_qwen3_asr",
        },
        "history": [
            {"kind": "attempt", "id": 2, "execution_kind": "initial", "status": "completed",
             "settings_snapshot": '{"DryRun":false,"Translate":{"APIKey":"SECRET_SENTINEL"}}'},
            {"kind": "preparation", "id": 1, "submission_id": "sub-current", "status": "resolved"},
        ],
    }


class VerificationTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name) / "媒体 root"
        self.root.mkdir()
        self.sample = self.root / "torrents/whisparr/VERIFY-123.mp4"
        self.sample.parent.mkdir(parents=True)
        self.sample.write_bytes(b"fixture-not-real-media")
        self.detail = completed()
        self.preparation = "resolved"
        self.run_id = 1
        self.sub_polls = self.job_polls = 0
        self.requests = []
        self.mutate = lambda kind, body: body
        self.http_status = 200
        self.post_status = 202
        self.ack = {"submission_id": "sub-current"}
        self.post_delay = 0
        for path in ("scraping/VERIFY-123.mp4", "subtitles/VERIFY-123.srt",
                     "scraping/动态 name.mkv", "subtitles/动态 name.zh.srt", "transcriptions/动态 name.srt"):
            target = self.root / "automation" / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(SRT)
        fixture = self

        class Handler(BaseHTTPRequestHandler):
            def reply(self, status, body):
                data = json.dumps(body).encode() if not isinstance(body, bytes) else body
                self.send_response(status)
                if status == 302:
                    self.send_header("Location", "/redirect")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                try:
                    self.wfile.write(data)
                except (BrokenPipeError, ConnectionResetError):
                    pass

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                fixture.requests.append(("POST", self.path, body))
                time.sleep(fixture.post_delay)
                self.reply(fixture.post_status, fixture.ack)

            def do_GET(self):
                fixture.requests.append(("GET", self.path))
                if self.path == "/api/v1/version":
                    return self.reply(200, {"version": "contract-fixture"})
                if self.path == "/api/v1/health":
                    return self.reply(fixture.http_status, {"status": "ok"})
                if self.path.startswith("/api/v1/tasks/submission/"):
                    fixture.sub_polls += 1
                    body = {
                        "kind": "submission", "id": "sub-current", "status": "completed",
                        "execution_id": fixture.detail["execution_id"],
                        "submission": {"id": "sub-current", "status": fixture.preparation,
                                       "run_id": fixture.run_id, "job_id": "job-current",
                                       "created_at": "2026-01-01T00:00:00.000000001Z"},
                    }
                    if fixture.preparation == "resolved":
                        body["canonical_task"] = {"kind": "job", "id": "job-current"}
                    body = fixture.mutate("submission", body)
                elif self.path.startswith("/api/v1/tasks/job/job-current"):
                    fixture.job_polls += 1
                    body = fixture.mutate("job", copy.deepcopy(fixture.detail))
                else:
                    return self.reply(404, {})
                self.reply(fixture.http_status, body)

            def log_message(self, *_):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=self.server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        thread.start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(thread.join)
        self.addCleanup(self.server.shutdown)
        self.url = "http://127.0.0.1:%d" % self.server.server_port

    def invoke(self, expect="subtitles", command="run", extra=(), timeout="4"):
        args = [sys.executable, str(SCRIPT), command, "--url", self.url]
        if command != "health":
            args += ["--expect", expect, "--host-root", str(self.root), "--timeout", timeout,
                     "--poll-interval", "0.01"]
            if command == "run":
                args += ["--sample", str(self.sample), "--name", "VERIFY-123"]
            else:
                args += ["--submission-id", "sub-current"]
        proc = subprocess.run([*args, *extra], capture_output=True, text=True, timeout=8)
        result = json.loads(proc.stdout)
        self.assertNotIn("SECRET_SENTINEL", proc.stdout + proc.stderr)
        self.assertEqual(result["claims"]["asr_invoked_this_run"], "not_proven")
        self.assertEqual(result["claims"]["llm_invoked_this_run"], "not_proven")
        self.assertFalse(any("VERIFY-123" in row[1] for row in self.requests))
        self.assertFalse(any("actions" in row[1] for row in self.requests))
        return proc.returncode, result

    def assert_reason(self, code, reason, **kwargs):
        actual, result = self.invoke(**kwargs)
        self.assertEqual((actual, result["reason"]), (code, reason), result)
        return result

    def test_success_uses_dynamic_paths_and_never_claims_model_calls(self):
        code, result = self.invoke()
        self.assertEqual(code, 0, result)
        self.assertEqual(result["claims"]["subtitle_artifacts_observed"], "proven")
        self.assertEqual(result["artifacts"]["video"]["path"], self.detail["job"]["processing_path"])
        posts = [row for row in self.requests if row[0] == "POST"]
        self.assertEqual(len(posts), 1)
        self.assertEqual(posts[0][2]["path"], "/data/torrents/whisparr/VERIFY-123.mp4")

    def test_health_and_observe_never_post(self):
        for command in ("health", "observe"):
            code, result = self.invoke(command=command)
            self.assertEqual(code, 0, result)
        self.assertFalse(any(row[0] == "POST" for row in self.requests))

    def test_accepted_is_not_preparation_success(self):
        self.preparation = "failed"
        code, result = self.invoke(expect="accepted")
        self.assertEqual(code, 0)
        self.assertEqual(result["preparation_status"], "failed")
        self.assertEqual(result["claims"]["delivery_observed"], "not_proven")

    def test_preparation_terminal_states_never_query_old_jobs(self):
        for state in ("failed", "ignored", "interrupted"):
            with self.subTest(state=state):
                self.preparation = state
                self.assert_reason(1, "preparation_" + state)
        self.assertEqual(self.job_polls, 0)

    def test_preparation_timeout(self):
        self.preparation = "preparing"
        self.assert_reason(2, "timeout", command="observe", timeout="0.1")
        self.assertEqual(self.job_polls, 0)

    def test_resolved_submission_top_level_is_not_preparation_status(self):
        def mutate(kind, body):
            if kind == "submission":
                body["status"] = "failed"  # projection, not preparation
            return body
        self.mutate = mutate
        code, result = self.invoke()
        self.assertEqual(code, 0, result)

    def test_acknowledgements_and_category(self):
        self.assert_reason(2, "category_skipped", extra=("--category", "radarr"))
        self.assertFalse(any(row[0] == "POST" for row in self.requests))
        for status, ack, code, reason in (
            (202, {}, 2, "submission_outcome_unknown"),
            (202, b"not-json", 2, "submission_outcome_unknown"),
            (503, {}, 1, "submission_rejected"),
            (302, {}, 1, "submission_rejected"),
        ):
            with self.subTest(status=status, ack=ack):
                self.requests.clear()
                self.post_status, self.ack = status, ack
                self.assert_reason(code, reason)
                self.assertEqual(sum(row[0] == "POST" for row in self.requests), 1)

    def test_post_timeout_preserves_request_id_without_retry(self):
        self.post_delay = 0.15
        result = self.assert_reason(2, "submission_outcome_unknown", extra=("--request-timeout", "0.02"))
        self.assertTrue(result["request_id"].startswith("verify-"))
        self.assertEqual(sum(row[0] == "POST" for row in self.requests), 1)

    def test_get_errors(self):
        for status in (404, 500, 302):
            with self.subTest(status=status):
                self.http_status = status
                self.assert_reason(2, "http_error", command="observe")

    def test_invalid_json_and_bounded_response(self):
        for payload, reason in ((b"not-json", "invalid_json"), (b"[]", "incompatible_response"),
                                (b" " * (4 * 1024 * 1024 + 1), "response_too_large")):
            with self.subTest(reason=reason):
                self.mutate = lambda _, body: payload
                self.assert_reason(2, reason, command="observe")

    def test_invalid_arguments_do_not_submit(self):
        for extra in (("--timeout", "nan"), ("--poll-interval", "0"), ("--url", "http://user:password@localhost")):
            code, result = self.invoke(extra=extra)
            self.assertEqual(code, 2, result)
        self.assertEqual(self.requests, [])

    def test_malformed_or_inconsistent_fields(self):
        original = completed()
        cases = (
            (lambda d: d.pop("is_light"), "incompatible_response"),
            (lambda d: d.update(is_light="false"), "incompatible_response"),
            (lambda d: d["job"].update(id="other"), "identity_mismatch"),
            (lambda d: d["job"].update(attempt_id=3), "identity_mismatch"),
            (lambda d: d.update(status="transcribing"), "incompatible_response"),
            (lambda d: d.pop("stage"), "incompatible_response"),
            (lambda d: d["history"][1].update(id=True), "incompatible_response"),
            (lambda d: d.update(persistence_error="SECRET_SENTINEL"), "persistence_error"),
            (lambda d: d["job"].update(checkpoint="translated"), "incompatible_response"),
        )
        for change, reason in cases:
            with self.subTest(reason=reason, change=change):
                self.detail = copy.deepcopy(original)
                change(self.detail)
                self.assert_reason(2, reason, command="observe")

    def test_submission_identity_and_association_mismatch(self):
        for key in ("id", "job_id"):
            def mutate(kind, body):
                if kind == "submission":
                    body["submission"][key] = "different"
                return body
            self.mutate = mutate
            self.assert_reason(2, "identity_mismatch", command="observe")

    def test_old_completed_job_and_noninitial_attempt(self):
        self.detail["job"]["created_at"] = "2026-01-01T00:00:00Z"
        self.assert_reason(2, "existing_job")
        self.detail = completed()
        self.detail["execution_kind"] = self.detail["job"]["attempt_kind"] = "automatic_retry"
        self.assert_reason(2, "non_initial_execution")

    def test_latest_failure_overrides_retained_completed(self):
        for state in ("failed", "interrupted"):
            self.detail["status"] = self.detail["job"]["attempt_status"] = state
            self.assert_reason(1, "execution_" + state)

    def test_light_with_and_without_sidecar(self):
        self.detail["is_light"] = self.detail["job"]["is_light"] = True
        for path in ("", "/data/automation/subtitles/动态 name.zh.srt"):
            self.detail["job"]["translated_path"] = path
            code, result = self.invoke(expect="delivered")
            self.assertEqual(code, 0, result)
            self.assert_reason(2, "sample_bypassed")

    def test_dry_run_and_missing_snapshot(self):
        for snapshot, reason in (("{}", "settings_unavailable"), ("", "settings_unavailable"),
                                 ('{"DryRun":true}', "dry_run"), ('{"dry_run":false}', "settings_unavailable")):
            self.detail["history"][0]["settings_snapshot"] = snapshot
            self.assert_reason(2, reason)

    def test_dynamic_files_missing_despite_old_fixed_files(self):
        for folder, filename in (("scraping", "动态 name.mkv"), ("subtitles", "动态 name.zh.srt"),
                                 ("transcriptions", "动态 name.srt")):
            with self.subTest(folder=folder):
                target = self.root / "automation" / folder / filename
                target.unlink()
                self.assert_reason(1, "artifact_missing")
                target.write_text(SRT)

    def test_empty_or_invalid_srt(self):
        target = self.root / "automation/subtitles/动态 name.zh.srt"
        for text, reason in (("", "artifact_empty_or_not_file"), ("old arbitrary text", "invalid_srt"),
                             (SRT.replace("00:00:01,000", "00:00:00,000"), "invalid_srt")):
            target.write_text(text)
            self.assert_reason(1, reason)

    def test_path_traversal_prefix_and_symlink_escape(self):
        for path in ("/database/automation/scraping/动态 name.mkv",
                     "/data/automation/scraping/../scraping/动态 name.mkv", "relative.mkv"):
            self.detail["job"]["processing_path"] = path
            self.assert_reason(2, "unmapped_artifact")
        self.detail = completed()
        target = self.root / "automation/scraping/动态 name.mkv"
        target.unlink()
        target.symlink_to(self.sample)
        self.assert_reason(2, "unmapped_artifact")

    def test_changes_during_final_recheck(self):
        for change, reason in (("run", "execution_changed"), ("attempt", "execution_changed"),
                               ("path", "artifacts_changed")):
            self.sub_polls = self.job_polls = 0
            def mutate(kind, body):
                if change == "run" and kind == "submission" and self.sub_polls > 1:
                    body["submission"]["run_id"] = 20
                if kind == "job" and self.job_polls > 1:
                    if change == "attempt":
                        body["execution_id"] = body["job"]["attempt_id"] = 20
                    elif change == "path":
                        body["job"]["translated_path"] = "/data/automation/subtitles/other.srt"
                return body
            self.mutate = mutate
            self.assert_reason(2, reason)

    def test_history_pagination_and_ambiguous_link(self):
        def mutate(kind, body):
            if kind == "job":
                if "history_cursor" in self.requests[-1][1]:
                    body["history"] = body["history"][1:]
                else:
                    body["history"] = body["history"][:1]
                    body["next_history_cursor"] = "page-two"
            return body
        self.mutate = mutate
        code, result = self.invoke()
        self.assertEqual(code, 0, result)
        self.mutate = lambda _, body: body
        self.detail["history"].append({"kind": "preparation", "id": 3, "submission_id": "other"})
        self.assert_reason(2, "ambiguous_history")

    def test_history_missing_or_looping(self):
        self.detail["history"] = []
        self.assert_reason(2, "ambiguous_history")
        self.detail["next_history_cursor"] = "same-page"
        self.assert_reason(2, "history_unavailable")

    def test_pending_job_and_no_middle_stage_requirement(self):
        def mutate(kind, body):
            if kind == "job" and self.job_polls == 1:
                body["status"] = body["job"]["attempt_status"] = "pending"
            return body
        self.mutate = mutate
        code, result = self.invoke()
        self.assertEqual(code, 0, result)
        self.assertGreaterEqual(self.job_polls, 3)

    def test_old_hook_or_polluted_stdout_not_parsed_as_logs(self):
        hook = self.root / "old-hook.py"
        hook.write_text('print("event=webhook_sent submission_id=sub-current")\n')
        self.assert_reason(2, "invalid_hook_receipt", extra=("--hook", str(hook)))
        self.assertFalse(any(row[0] == "POST" for row in self.requests))

    def test_tutorial_examples_call_the_same_cli(self):
        for name in ("macos-deployment.md", "agent-deployment.md", "linux-deployment.md"):
            content = (ROOT / "docs/deployment" / name).read_text()
            blocks = re.findall(r"<!-- muse-verification-example -->\n```bash\n(.*?)\n```", content, re.S)
            self.assertTrue(blocks, name)
            for block in blocks:
                env = {**os.environ, "MUSE_URL": self.url, "MEDIA_ROOT": str(self.root),
                       "SAMPLE_VIDEO": str(self.sample), "SAMPLE_NAME": "VERIFY-123", "VERIFY_TIMEOUT": "4"}
                proc = subprocess.run(["bash", "-c", block], cwd=ROOT, env=env,
                                      capture_output=True, text=True, timeout=8)
                self.assertEqual(proc.returncode, 0, (name, proc.stderr, proc.stdout))
                result = json.loads(proc.stdout)
                self.assertEqual(result["claims"]["asr_invoked_this_run"], "not_proven")


if __name__ == "__main__":
    unittest.main()

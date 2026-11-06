#!/usr/bin/env python3
"""Verify Muse receipts and artifacts, never infer actual model invocations.

Run --help for the shared tutorial/agent/test interface. Only `run` submits
media, exactly once through torrent_complete.py. All other requests are GETs.
"""

import argparse
import datetime
import hashlib
import http.client
import json
import math
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

VERSION = "1"
MAX_RESPONSE_BYTES = 4 * 1024 * 1024
MAX_SUBTITLE_BYTES = 32 * 1024 * 1024
MAX_HISTORY_PAGES = 100
ID = re.compile(r"[A-Za-z0-9._-]{1,64}\Z")
STATES = {"pending", "processing", "completed", "failed", "interrupted"}
PREPARATION_STATES = {"pending", "preparing", "resolved", "failed", "interrupted", "ignored"}


class VerificationError(Exception):
    def __init__(self, reason, code=2):
        super().__init__(reason)
        self.reason = reason
        self.code = code


def require(condition, reason="incompatible_response", code=2):
    if not condition:
        raise VerificationError(reason, code)


def field(obj, key, kind):
    require(isinstance(obj, dict) and type(obj.get(key)) is kind)
    return obj[key]


def identifier(value):
    require(isinstance(value, str) and ID.fullmatch(value), "invalid_identifier")
    return value


def positive_number(value):
    try:
        number = float(value)
        require(math.isfinite(number) and number > 0, "invalid_arguments")
        return number
    except ValueError:
        raise VerificationError("invalid_arguments") from None


def timestamp(value):
    # Preserve Go RFC3339Nano precision when rejecting older Jobs.
    match = re.fullmatch(r"(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)", value)
    require(match is not None)
    try:
        dt = datetime.datetime.fromisoformat(match[1] + match[3].replace("Z", "+00:00"))
        return int(dt.timestamp()) * 10**9 + int((match[2] or "").ljust(9, "0"))
    except (ValueError, OverflowError):
        raise VerificationError("incompatible_response") from None


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise VerificationError("invalid_arguments")


def arguments(argv):
    parser = Parser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("health", "run", "observe"):
        command = commands.add_parser(name)
        command.add_argument("--url", required=True, help="Muse base URL, without credentials")
        command.add_argument("--request-timeout", type=positive_number, default=15.0)
        command.add_argument("--timeout", type=positive_number, help="total seconds; required for processing targets")
        if name == "health":
            command.add_argument("--asr-url", help="optional HostASR URL, checked from this machine only")
            continue
        command.add_argument("--expect", choices=("accepted", "delivered", "subtitles"), required=True)
        command.add_argument("--host-root", help="local directory mounted at --container-root")
        command.add_argument("--container-root", default="/data")
        command.add_argument("--poll-interval", type=positive_number, default=2.0)
        if name == "run":
            command.add_argument("--sample", required=True)
            command.add_argument("--name", required=True)
            command.add_argument("--category", default="whisparr")
            command.add_argument("--hook", default=str(Path(__file__).with_name("torrent_complete.py")))
        else:
            command.add_argument("--submission-id", required=True)
    args = parser.parse_args(argv)
    if args.command != "health":
        require(args.expect == "accepted" or args.timeout is not None, "invalid_arguments")
        require(args.host_root or args.command == "observe" and args.expect == "accepted", "invalid_arguments")
    args.timeout = args.timeout or 30.0
    return args


def base_url(value):
    parsed = urllib.parse.urlsplit(value)
    require(parsed.scheme in ("http", "https") and parsed.hostname and not parsed.username
            and not parsed.password and not parsed.query and not parsed.fragment
            and parsed.path in ("", "/"), "invalid_url")
    return value.rstrip("/")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Verification:
    def __init__(self, args, result):
        self.args, self.result = args, result
        self.url = base_url(args.url)
        self.deadline = time.monotonic() + args.timeout
        self.opener = urllib.request.build_opener(NoRedirect)
        self.run_id = self.job_id = self.execution_id = None
        self.root = self.container = None
        if getattr(args, "host_root", None):
            self.root = Path(args.host_root).resolve()
            require(self.root.is_dir(), "unreadable_host_root")
            self.container = PurePosixPath(args.container_root)
            require(self.container.is_absolute() and ".." not in self.container.parts
                    and not str(self.container).startswith("//"), "invalid_container_root")

    def remaining(self):
        left = self.deadline - time.monotonic()
        require(left > 0, "timeout")
        return left

    def get(self, path, url=None):
        try:
            with self.opener.open((url or self.url) + path,
                                  timeout=min(self.args.request_timeout, self.remaining())) as response:
                require(response.status == 200, "http_error")
                # Read incrementally so a trickling peer cannot extend the total deadline.
                chunks, size = [], 0
                while True:
                    self.remaining()
                    chunk = response.read1(min(65536, MAX_RESPONSE_BYTES + 1 - size))
                    if not chunk:
                        break
                    chunks.append(chunk)
                    size += len(chunk)
                    require(size <= MAX_RESPONSE_BYTES, "response_too_large")
                data = json.loads(b"".join(chunks))
                require(isinstance(data, dict))
                self.remaining()
                return data
        except urllib.error.HTTPError as error:
            self.result["http_status"] = error.code
            error.close()
            raise VerificationError("http_error") from None
        except (OSError, http.client.HTTPException):
            raise VerificationError("transport_error") from None
        except (ValueError, UnicodeError):
            raise VerificationError("invalid_json") from None

    def task(self, kind, identity, cursor=None):
        path = "/api/v1/tasks/" + kind + "/" + identifier(identity)
        if cursor:
            path += "?" + urllib.parse.urlencode({"history_cursor": cursor})
        detail = self.get(path)
        require(detail.get("kind") == kind and detail.get("id") == identity, "identity_mismatch")
        require(not detail.get("persistence_error"), "persistence_error")
        return detail

    def pause(self):
        time.sleep(min(self.args.poll_interval, self.remaining()))

    def submit(self):
        sample = Path(self.args.sample).resolve()
        require(sample.is_file(), "invalid_sample")
        require(sample.is_relative_to(self.root), "path_outside_mount")
        hook = Path(self.args.hook).resolve()
        require(hook.is_file(), "hook_unavailable")
        self.result["hook_sha256"] = hashlib.sha256(hook.read_bytes()).hexdigest()
        request_id = "verify-" + uuid.uuid4().hex
        self.result["request_id"] = request_id
        environment = {**os.environ, "MUSE_URL": self.url, "HOST_INPUT_PATH": str(self.root),
                       "CONTAINER_INPUT_PATH": str(self.container), "LOG_FILE": os.devnull,
                       "MUSE_REQUEST_ID": request_id,
                       "MUSE_REQUEST_TIMEOUT": str(min(self.args.request_timeout, self.remaining()))}
        try:
            child = subprocess.run(
                [sys.executable, str(hook), "--receipt-json", self.args.name, self.args.category,
                 request_id, str(sample)], env=environment, capture_output=True, timeout=self.remaining())
        except subprocess.TimeoutExpired:
            raise VerificationError("submission_outcome_unknown") from None
        try:
            receipt = json.loads(child.stdout)
        except (ValueError, UnicodeError):
            raise VerificationError("invalid_hook_receipt") from None
        require(isinstance(receipt, dict) and type(receipt.get("schema_version")) is int
                and receipt["schema_version"] == 1 and receipt.get("request_id") == request_id,
                "invalid_hook_receipt")
        outcome = receipt.get("result")
        if outcome == "skipped":
            raise VerificationError("category_skipped")
        if outcome == "rejected":
            raise VerificationError("submission_rejected", 1)
        require(outcome == "accepted", "submission_outcome_unknown")
        require(child.returncode == 0 and receipt.get("http_status") == 202, "invalid_hook_receipt")
        return identifier(receipt.get("submission_id"))

    def submission(self, sid):
        detail = self.task("submission", sid)
        submission = field(detail, "submission", dict)
        require(submission.get("id") == sid, "identity_mismatch")
        run_id = field(submission, "run_id", int)
        require(run_id > 0)
        if self.run_id is None:
            self.run_id = run_id
        require(run_id == self.run_id, "execution_changed")
        status = field(submission, "status", str)
        require(status in PREPARATION_STATES)
        self.result.update(preparation_run_id=run_id, preparation_status=status,
                           observed_submission_status=field(detail, "status", str))
        self.result["claims"]["submission_persisted"] = "proven"
        if status == "resolved":
            canonical = field(detail, "canonical_task", dict)
            jid = identifier(canonical.get("id"))
            require(canonical.get("kind") == "job" and submission.get("job_id") == jid,
                    "identity_mismatch")
            execution_id = field(detail, "execution_id", int)
            require(execution_id > 0)
            if self.job_id is None:
                self.job_id, self.execution_id = jid, execution_id
            require((jid, execution_id) == (self.job_id, self.execution_id), "execution_changed")
            self.result.update(job_id=jid, execution_id=execution_id)
        else:
            require(not detail.get("canonical_task"), "identity_mismatch")
            require(self.job_id is None, "execution_changed")
        return submission

    def job(self, detail=None):
        detail = detail or self.task("job", self.job_id)
        job = field(detail, "job", dict)
        require(job.get("id") == self.job_id, "identity_mismatch")
        eid = field(detail, "execution_id", int)
        require(eid == self.execution_id, "execution_changed")
        require(field(job, "attempt_id", int) == eid, "identity_mismatch")
        status = field(detail, "status", str)
        require(status in STATES and job.get("attempt_status") == status)
        kind = field(detail, "execution_kind", str)
        require(job.get("attempt_kind") == kind)
        light = field(job, "is_light", bool)
        require(field(detail, "is_light", bool) == light)
        require(not job.get("persistence_error"), "persistence_error")
        self.result.update(status=status, execution_kind=kind, is_light=light,
                           stage=field(detail, "stage", str))
        detection = job.get("subtitle_detection_reason", "")
        require(isinstance(detection, str))
        if detection in ("filename", "sidecar_subtitle", "embedded_subtitle", "hard_sub_ocr"):
            self.result["subtitle_detection_reason"] = detection
        if status in ("failed", "interrupted"):
            raise VerificationError("execution_" + status, 1)
        require(kind == "initial", "non_initial_execution")
        if status == "completed":
            require(detail.get("job_status") == job.get("status") == "completed"
                    and job.get("checkpoint") == detail.get("stage") == "delivered")
        return detail

    def history(self, detail):
        entries, cursors = [], set()
        for _ in range(MAX_HISTORY_PAGES):
            self.job(detail)
            require(detail["status"] == "completed", "execution_changed")
            entries.extend(field(detail, "history", list))
            cursor = detail.get("next_history_cursor", "")
            require(isinstance(cursor, str))
            if not cursor:
                return entries
            require(cursor not in cursors, "history_unavailable")
            cursors.add(cursor)
            detail = self.task("job", self.job_id, cursor)
        raise VerificationError("history_unavailable")

    def execution_evidence(self, detail, sid):
        entries = self.history(detail)
        require(all(isinstance(entry, dict) for entry in entries))
        preparations = [entry for entry in entries if entry.get("kind") == "preparation"]
        attempts = [entry for entry in entries if entry.get("kind") == "attempt"]
        require(len(preparations) == len(attempts) == 1 and len(entries) == 2, "ambiguous_history")
        preparation, attempt = preparations[0], attempts[0]
        require(field(preparation, "id", int) == self.run_id and preparation.get("submission_id") == sid
                and preparation.get("status") == "resolved", "ambiguous_history")
        require(field(attempt, "id", int) == self.execution_id and attempt.get("execution_kind") == "initial"
                and attempt.get("status") == "completed", "ambiguous_history")
        if self.args.expect == "subtitles":
            require(not detail["job"]["is_light"], "sample_bypassed")
            try:
                snapshot = json.loads(field(attempt, "settings_snapshot", str))
            except (ValueError, UnicodeError):
                raise VerificationError("settings_unavailable") from None
            require(isinstance(snapshot, dict) and type(snapshot.get("DryRun")) is bool, "settings_unavailable")
            require(not snapshot["DryRun"], "dry_run")
            require(bool(field(detail["job"], "transcription_source", str)), "source_unavailable")

    def artifact(self, value, folder, srt=False):
        require(isinstance(value, str) and value, "artifact_path_missing", 1)
        path = PurePosixPath(value)
        require(path.is_absolute() and ".." not in path.parts
                and not value.startswith("//"), "unmapped_artifact")
        expected = self.container / "automation" / folder
        require(path.is_relative_to(expected), "unmapped_artifact")
        local = (self.root / path.relative_to(self.container)).resolve()
        allowed = self.root / "automation" / folder
        require(local.is_relative_to(allowed), "unmapped_artifact")
        try:
            info = local.stat()
            require(stat.S_ISREG(info.st_mode) and info.st_size > 0, "artifact_empty_or_not_file", 1)
            with local.open("rb") as stream:
                if srt:
                    require(info.st_size <= MAX_SUBTITLE_BYTES, "subtitle_too_large")
                    data = stream.read(MAX_SUBTITLE_BYTES + 1)
                    require(len(data) <= MAX_SUBTITLE_BYTES, "subtitle_too_large")
                    validate_srt(data)
                else:
                    require(bool(stream.read(1)), "artifact_empty_or_not_file", 1)
            observed = {"path": value, "local_path": str(local), "size": info.st_size}
            if srt:
                observed["sha256"] = hashlib.sha256(data).hexdigest()
            return observed
        except FileNotFoundError:
            raise VerificationError("artifact_missing", 1) from None
        except (OSError, ValueError):
            raise VerificationError("artifact_unreadable") from None

    def artifacts(self, job):
        artifacts = {"video": self.artifact(job.get("processing_path"), "scraping")}
        translated = job.get("translated_path", "")
        require(isinstance(translated, str))
        if translated or self.args.expect == "subtitles":
            artifacts["translated"] = self.artifact(translated, "subtitles", self.args.expect == "subtitles")
        if self.args.expect == "subtitles":
            artifacts["source"] = self.artifact(job.get("subtitle_path"), "transcriptions", True)
        return artifacts

    def execute(self):
        if self.args.command == "health":
            require(self.get("/api/v1/health").get("status") == "ok", "health_unexpected")
            self.result["muse_http_accessible"] = True
            if self.args.asr_url:
                require(self.get("/health", base_url(self.args.asr_url)).get("status") == "healthy",
                        "asr_health_unexpected")
                self.result["asr_http_accessible_from_checker"] = True
        version = field(self.get("/api/v1/version"), "version", str)
        self.result["muse_version"] = version
        if self.args.command == "health":
            return
        sid = self.submit() if self.args.command == "run" else identifier(self.args.submission_id)
        self.result["submission_id"] = sid
        while True:
            submission = self.submission(sid)
            if self.args.expect == "accepted":
                return
            status = submission["status"]
            if status in ("failed", "ignored", "interrupted"):
                raise VerificationError("preparation_" + status, 1)
            if status == "resolved":
                detail = self.job()
                require(timestamp(field(detail["job"], "created_at", str)) >=
                        timestamp(field(submission, "created_at", str)), "existing_job")
                if detail["status"] == "completed":
                    break
            self.pause()
        self.execution_evidence(detail, sid)
        observed = self.artifacts(detail["job"])
        self.submission(sid)
        final = self.job()
        require(final["status"] == "completed", "execution_changed")
        keys = ("processing_path", "subtitle_path", "translated_path", "is_light", "transcription_source")
        require(all(final["job"].get(key) == detail["job"].get(key) for key in keys), "artifacts_changed")
        self.execution_evidence(final, sid)
        self.remaining()
        self.result["artifacts"] = observed
        self.result["claims"]["delivery_observed"] = "proven"
        if self.args.expect == "subtitles":
            self.result["claims"]["subtitle_artifacts_observed"] = "proven"


def validate_srt(data):
    try:
        text = data.decode("utf-8-sig").replace("\r\n", "\n").strip()
    except UnicodeError:
        raise VerificationError("invalid_srt", 1) from None
    blocks = re.split(r"\n\s*\n", text)
    pattern = re.compile(r"(\d{2,}):([0-5]\d):([0-5]\d),(\d{3}) --> (\d{2,}):([0-5]\d):([0-5]\d),(\d{3})\Z")
    for block in blocks:
        lines = block.splitlines()
        require(len(lines) >= 3 and lines[0].isdigit(), "invalid_srt", 1)
        match = pattern.fullmatch(lines[1])
        require(match is not None and any(line.strip() for line in lines[2:]), "invalid_srt", 1)
        times = [int(value) for value in match.groups()]
        start, end = [((times[i] * 60 + times[i + 1]) * 60 + times[i + 2]) * 1000 + times[i + 3]
                      for i in (0, 4)]
        require(end > start, "invalid_srt", 1)


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def main(argv=None):
    result = {"schema_version": 1, "verifier_version": VERSION, "started_at": now(),
              "claims": {key: "not_proven" for key in (
                  "submission_persisted", "delivery_observed", "subtitle_artifacts_observed",
                  "asr_invoked_this_run", "llm_invoked_this_run")}}
    code = 0
    try:
        args = arguments(argv)
        result["expectation"] = "health" if args.command == "health" else args.expect
        print("正在核对所选目标；实际模型调用需要另行留证。", file=sys.stderr)
        Verification(args, result).execute()
        result.update(outcome="pass", reason="target_verified")
    except VerificationError as error:
        code = error.code
        result.update(outcome="fail" if code == 1 else "inconclusive", reason=error.reason)
    except KeyboardInterrupt:
        code = 2
        result.update(outcome="inconclusive", reason="cancelled")
    except (OSError, ValueError, RuntimeError):
        # Do not print exception text: paths/HTTP errors can carry private input.
        code = 2
        result.update(outcome="inconclusive", reason="local_environment_error")
    result["finished_at"] = now()
    print(json.dumps(result, ensure_ascii=False))
    return code


if __name__ == "__main__":
    sys.exit(main())

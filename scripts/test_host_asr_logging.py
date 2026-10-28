#!/usr/bin/env python3
"""Behaviour tests for the ASR host service.

FastAPI, uvicorn and the MLX tools are not required: the service is imported
against minimal stubs, and the tool call is replaced. This exercises the real
handler, the real middleware and the real logging policy.
"""

import io
import logging
import pathlib
import sys
import tempfile
import types
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

import re  # noqa: E402

import logfmt  # noqa: E402


def install_stubs():
    fastapi = types.ModuleType("fastapi")

    class Request:
        def __init__(self, method="GET", path="/", headers=None):
            self.method = method
            self.url = types.SimpleNamespace(path=path)
            self.headers = headers or {}
            self.scope = {}
            self.state = types.SimpleNamespace()

    class Response:
        def __init__(self):
            self.status_code = 200
            self.headers = {}

    class JSONResponse:
        def __init__(self, status_code=200, content=None):
            self.status_code = status_code
            self.content = content
            self.headers = {}

    class FastAPI:
        def __init__(self, **kwargs):
            self.routes = {}
            self.http_middleware = None

        def middleware(self, _kind):
            def decorator(function):
                self.http_middleware = function
                return function

            return decorator

        def _route(self, method, path):
            def decorator(function):
                self.routes[(method, path)] = function
                return function

            return decorator

        def get(self, path, **_kwargs):
            return self._route("GET", path)

        def post(self, path, **_kwargs):
            return self._route("POST", path)

    fastapi.FastAPI = FastAPI
    fastapi.JSONResponse = JSONResponse
    fastapi.Request = Request
    fastapi.Response = Response

    uvicorn = types.ModuleType("uvicorn")
    uvicorn.run = lambda *args, **kwargs: None

    sys.modules["fastapi"] = fastapi
    sys.modules["uvicorn"] = uvicorn
    return fastapi


install_stubs()
with mock.patch("sys.stderr", io.StringIO()):
    import host_asr_server as service  # noqa: E402


class Request:
    def __init__(self, method="GET", path="/", headers=None):
        self.method = method
        self.url = types.SimpleNamespace(path=path)
        self.headers = headers or {}
        self.scope = {}
        self.state = types.SimpleNamespace()


class Response:
    def __init__(self):
        self.status_code = 200
        self.headers = {}


class HostASRLoggingTest(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.stream = io.StringIO()
        service.log = logfmt.configure(service="host-asr", component="host-asr", stream=self.stream)
        service._asr_lock = __import__("threading").Lock()
        logfmt._third_party_spent = 0
        self.addCleanup(self._restore_root)
        self.root = logging.getLogger()
        self.saved = list(self.root.handlers)

    def _restore_root(self):
        self.root.handlers[:] = self.saved

    def output(self):
        return self.stream.getvalue()

    async def call_middleware(self, request, handler_response):
        async def call_next(_request):
            return handler_response

        return await service.request_middleware(request, call_next)

    async def test_handler_exception_is_reported_once_with_a_response(self):
        async def call_next(_request):
            raise RuntimeError("REVIEW_ONLY_EXCEPTION_TEXT")

        request = Request("POST", "/transcribe", {"x-request-id": "req_boom"})
        returned = await service.request_middleware(request, call_next)

        self.assertEqual(returned.status_code, 500)
        self.assertEqual(returned.headers["X-Request-Id"], "req_boom")
        output = self.output()
        self.assertEqual(output.count("event=request_completed"), 1, output)
        self.assertIn("ERROR service=host-asr component=host-asr event=request_completed", output)
        self.assertIn("request_id=req_boom", output)
        self.assertIn("status=500", output)
        self.assertIn("stack=", output)
        self.assertNotIn("REVIEW_ONLY_EXCEPTION_TEXT", output)

    async def test_handler_events_carry_the_middleware_request_id(self):
        request = Request("POST", "/transcribe", {"x-request-id": "req_handler"})
        await self.call_middleware(request, Response())

        response = Response()
        result = await service.transcribe_video(
            service.TranscribeRequest(video_path="/data/missing.mp4", host_prefix="/tmp"), response, request
        )

        self.assertFalse(result.success)
        lines = [line for line in self.output().splitlines() if "event=video_missing" in line]
        self.assertEqual(len(lines), 1, self.output())
        self.assertIn("request_id=req_handler", lines[0])

    async def test_health_summary_is_debug_only(self):
        await self.call_middleware(Request("GET", "/health"), Response())
        self.assertEqual(self.output(), "", "a successful health check is not displayed at INFO")

    async def test_health_summary_is_visible_at_debug(self):
        service.log = logfmt.configure(
            service="host-asr", component="host-asr", stream=self.stream, level="DEBUG"
        )
        await self.call_middleware(Request("GET", "/health"), Response())
        self.assertIn("DEBUG service=host-asr component=host-asr event=request_completed", self.output())

    async def test_server_error_summary_is_error_level(self):
        response = Response()
        response.status_code = 500
        await self.call_middleware(Request("POST", "/transcribe"), response)
        self.assertIn("ERROR service=host-asr component=host-asr event=request_completed", self.output())
        self.assertIn("status=500", self.output())

    async def test_client_error_summary_is_warn_level(self):
        response = Response()
        response.status_code = 404
        await self.call_middleware(Request("POST", "/transcribe"), response)
        self.assertIn("WARN service=host-asr component=host-asr event=request_completed", self.output())
        self.assertIn("status=404", self.output())

    async def test_unknown_route_is_not_logged_as_a_raw_path(self):
        request = Request("GET", "/../../etc/passwd")
        await self.call_middleware(request, Response())
        self.assertIn('route=unmatched', self.output())
        self.assertNotIn("etc/passwd", self.output())

    async def test_valid_request_id_is_reused_and_echoed(self):
        request = Request("POST", "/transcribe", {"x-request-id": "abc-123_XY"})
        returned = await self.call_middleware(request, Response())
        self.assertEqual(returned.headers["X-Request-Id"], "abc-123_XY")
        self.assertIn("request_id=abc-123_XY", self.output())

    async def test_invalid_request_id_is_replaced(self):
        request = Request("POST", "/transcribe", {"x-request-id": "bad id!"})
        returned = await self.call_middleware(request, Response())
        self.assertNotEqual(returned.headers["X-Request-Id"], "bad id!")
        self.assertIn("request_id=", self.output())
        self.assertNotIn("bad id!", self.output())

    async def test_busy_response_contract_is_unchanged(self):
        service._asr_lock.acquire()
        try:
            response = Response()
            result = await service.transcribe_video(
                service.TranscribeRequest(video_path="/data/a.mp4", host_prefix="/tmp"), response
            )
        finally:
            service._asr_lock.release()
        self.assertEqual(response.status_code, 409)
        self.assertFalse(result.success)
        self.assertEqual(result.error, "ASR busy")
        self.assertIn("WARN service=host-asr component=host-asr event=transcription_busy", self.output())

    async def test_missing_video_contract_is_unchanged(self):
        response = Response()
        result = await service.transcribe_video(
            service.TranscribeRequest(video_path="/data/missing.mp4", host_prefix="/tmp"), response
        )
        self.assertEqual(response.status_code, 404)
        self.assertFalse(result.success)
        self.assertTrue(result.error.startswith("Video not found: "))
        self.assertIn("event=video_missing", self.output())

    async def test_success_path_contract_and_correlation_fields(self):
        with tempfile.TemporaryDirectory() as directory:
            video = pathlib.Path(directory, "movie.mp4")
            video.write_bytes(b"x")
            module = service
            real_run = module.subprocess.run

            def fake_run(cmd, **_kwargs):
                pathlib.Path(directory, "movie.srt").write_text("1\n")
                return types.SimpleNamespace(returncode=0, stdout="", stderr="")

            module.subprocess.run = fake_run
            try:
                response = Response()
                result = await module.transcribe_video(
                    module.TranscribeRequest(
                        video_path=str(video),
                        host_prefix=directory,
                        job_id="9f2c1e4a",
                        attempt=2,
                        attempt_id=42,
                    ),
                    response,
                )
            finally:
                module.subprocess.run = real_run

        self.assertTrue(result.success)
        self.assertTrue(result.output_path.endswith("movie.srt"))
        output = self.output()
        self.assertIn("event=transcription_completed", output)
        self.assertIn("job_id=9f2c1e4a attempt_id=42", output, "attempt_id is the durable id")
        self.assertIn("attempt_number=2", output, "attempt keeps its retry-ordinal meaning")

    async def drive_handled_tool_failure(self, level, stderr_text):
        """Run the real handler through the real middleware, as a request does."""
        with tempfile.TemporaryDirectory() as directory:
            video = pathlib.Path(directory, "movie.mp4")
            video.write_bytes(b"x")
            module = service
            real_run = module.subprocess.run

            def fake_run(cmd, **_kwargs):
                return types.SimpleNamespace(returncode=1, stdout="", stderr=stderr_text)

            module.subprocess.run = fake_run
            service.log = logfmt.configure(
                service="host-asr", component="host-asr", stream=self.stream, level=level
            )
            try:
                async def call_next(request):
                    response = Response()
                    await module.transcribe_video(
                        module.TranscribeRequest(video_path=str(video), host_prefix=directory),
                        response,
                        request,
                    )
                    return response

                request = Request("POST", "/transcribe", {"x-request-id": "req_asr_fail"})
                return await module.request_middleware(request, call_next)
            finally:
                module.subprocess.run = real_run

    async def test_handled_tool_failure_is_one_error_without_the_tool_output(self):
        canary = "ASR_RAW_BODY_REVIEW_CANARY"
        returned = await self.drive_handled_tool_failure("INFO", canary)

        self.assertEqual(returned.status_code, 500)
        output = self.output()
        self.assertEqual(output.count(" ERROR "), 1, output)
        self.assertEqual(output.count("event=request_completed"), 1, output)
        self.assertIn("ERROR service=host-asr component=host-asr event=transcription_failed", output)
        self.assertIn("exit_code=1", output)
        self.assertIn("WARN service=host-asr component=host-asr event=request_completed", output)
        self.assertIn("request_id=req_asr_fail", output)
        self.assertNotIn(canary, output, "INFO never carries the tool's own output")

    async def test_handled_tool_failure_shows_one_bounded_excerpt_at_debug(self):
        canary = "ASR_RAW_BODY_REVIEW_CANARY"
        returned = await self.drive_handled_tool_failure("DEBUG", (canary + " ") * 200)

        self.assertEqual(returned.status_code, 500)
        output = self.output()
        self.assertEqual(output.count(" ERROR "), 1, output)
        carried = [line for line in output.splitlines() if canary in line]
        self.assertEqual(len(carried), 1, output)
        self.assertIn("DEBUG", carried[0])
        self.assertIn("event=transcription_failed_detail", carried[0])
        match = re.search(r'preview="([^"]*)"', carried[0])
        self.assertIsNotNone(match, carried[0])
        # The excerpt is capped in Unicode characters; this sample is ASCII, so
        # the escaped length equals it.
        self.assertLessEqual(len(match.group(1)), logfmt.PREVIEW_LIMIT)

    async def test_tool_failure_keeps_the_real_reason_for_the_caller(self):
        canary = "ASR_RAW_BODY_REVIEW_CANARY"
        with tempfile.TemporaryDirectory() as directory:
            video = pathlib.Path(directory, "movie.mp4")
            video.write_bytes(b"x")
            module = service
            real_run = module.subprocess.run

            def fake_run(cmd, **_kwargs):
                return types.SimpleNamespace(returncode=2, stdout="", stderr=canary * 1000)

            module.subprocess.run = fake_run
            try:
                response = Response()
                result = await module.transcribe_video(
                    module.TranscribeRequest(video_path=str(video), host_prefix=directory), response
                )
            finally:
                module.subprocess.run = real_run

        self.assertEqual(response.status_code, 500)
        self.assertFalse(result.success)
        self.assertIn(canary, result.error, "the caller keeps the real reason")
        self.assertGreater(len(result.error), logfmt.FIELD_LIMIT)


if __name__ == "__main__":
    unittest.main()

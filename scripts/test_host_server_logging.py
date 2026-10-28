#!/usr/bin/env python3
"""Behaviour tests for the DuoSubs host service.

torch, sentence-transformers and duosubs are not required: the service is
imported against minimal stubs, so the real handler, the real middleware and the
real logging policy are exercised.
"""

import contextlib
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

import logfmt  # noqa: E402
from test_host_asr_logging import Response, install_stubs  # noqa: E402


def install_duosubs_stubs():
    torch = types.ModuleType("torch")
    torch.cuda = types.SimpleNamespace(is_available=lambda: False)
    torch.mps = types.SimpleNamespace(empty_cache=lambda: None)

    sentence_transformers = types.ModuleType("sentence_transformers")

    class SentenceTransformer:
        pass

    sentence_transformers.SentenceTransformer = SentenceTransformer

    duosubs = types.ModuleType("duosubs")

    class MergeArgs:
        def __init__(self, **kwargs):
            self.primary = kwargs.get("primary")
            self.secondary = kwargs.get("secondary")
            self.output_dir = kwargs.get("output_dir")
            self.output_name = kwargs.get("output_name")
            self.model = "stub-model"

    duosubs.MergeArgs = MergeArgs
    duosubs.load_sentence_transformer_model = lambda _args, _device: object()
    duosubs.load_subtitles = lambda _args: (None, None)
    duosubs.merge_subtitles = lambda *_args: None
    duosubs.save_subtitles_in_zip = lambda *_args: None

    sys.modules["torch"] = torch
    sys.modules["sentence_transformers"] = sentence_transformers
    sys.modules["duosubs"] = duosubs


class Request:
    def __init__(self, method="GET", path="/", headers=None):
        self.method = method
        self.url = types.SimpleNamespace(path=path)
        self.headers = headers or {}
        self.scope = {}
        self.state = types.SimpleNamespace()


install_stubs()
install_duosubs_stubs()
with mock.patch("sys.stderr", io.StringIO()):
    import host_server as service  # noqa: E402


class HostDuoSubsLoggingTest(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.stream = io.StringIO()
        service.log = logfmt.configure(service="duosubs", component="duosubs", stream=self.stream)
        logfmt._third_party_spent = 0
        self.root = logging.getLogger()
        self.saved = list(self.root.handlers)
        self.addCleanup(self._restore_root)

    def _restore_root(self):
        self.root.handlers[:] = self.saved

    def output(self):
        return self.stream.getvalue()

    async def call_middleware(self, request, response):
        async def call_next(_request):
            return response

        return await service.request_middleware(request, call_next)

    def merge_request(self, **overrides):
        values = {
            "primary_path": "/data/missing-primary.ass",
            "secondary_path": "/data/missing-secondary.ass",
            "output_dir": "/data/out",
            "host_prefix": "/tmp",
        }
        values.update(overrides)
        return service.MergeRequest(**values)

    async def test_handler_exception_is_reported_once_with_a_response(self):
        async def call_next(_request):
            raise RuntimeError("REVIEW_ONLY_EXCEPTION_TEXT")

        request = Request("POST", "/merge", {"x-request-id": "req_boom"})
        returned = await service.request_middleware(request, call_next)

        self.assertEqual(returned.status_code, 500)
        self.assertEqual(returned.headers["X-Request-Id"], "req_boom")
        output = self.output()
        self.assertEqual(output.count("event=request_completed"), 1, output)
        self.assertIn("ERROR service=duosubs component=duosubs event=request_completed", output)
        self.assertIn("status=500", output)
        self.assertIn("stack=", output)
        self.assertNotIn("REVIEW_ONLY_EXCEPTION_TEXT", output)

    async def test_handler_events_carry_the_middleware_request_id(self):
        request = Request("POST", "/merge", {"x-request-id": "req_handler"})
        await self.call_middleware(request, Response())

        result = await service.merge_subtitles(self.merge_request(), request)

        self.assertFalse(result.success)
        lines = [line for line in self.output().splitlines() if "event=input_missing" in line]
        self.assertEqual(len(lines), 1, self.output())
        self.assertIn("request_id=req_handler", lines[0])

    async def test_business_failure_keeps_one_summary_and_no_extra_error(self):
        """A failed merge answers 200 with success=false, so its failure is one line."""

        async def call_next(request):
            response = Response()
            await service.merge_subtitles(self.merge_request(), request)
            return response

        request = Request("POST", "/merge", {"x-request-id": "req_merge"})
        returned = await service.request_middleware(request, call_next)

        output = self.output()
        self.assertEqual(returned.status_code, 200)
        self.assertEqual(output.count("event=request_completed"), 1, output)
        self.assertEqual(output.count(" ERROR "), 0, output)
        self.assertIn("INFO service=duosubs component=duosubs event=request_completed", output)
        self.assertIn("event=input_missing", output)
        self.assertIn("request_id=req_merge", output)

    async def test_merge_summary_is_logged_and_request_id_echoed(self):
        request = Request("POST", "/merge", {"x-request-id": "req_1"})
        returned = await self.call_middleware(request, Response())
        self.assertEqual(returned.headers["X-Request-Id"], "req_1")
        self.assertIn("INFO service=duosubs component=duosubs event=request_completed", self.output())
        self.assertIn("request_id=req_1", self.output())
        self.assertIn('route="/merge"', self.output())

    async def test_unknown_route_is_not_logged_as_a_raw_path(self):
        await self.call_middleware(Request("GET", "/../secret"), Response())
        self.assertIn("route=unmatched", self.output())
        self.assertNotIn("secret", self.output())

    async def test_missing_primary_keeps_the_response_contract(self):
        result = await service.merge_subtitles(self.merge_request())
        self.assertFalse(result.success)
        self.assertTrue(result.error.startswith("Primary subtitle not found: "))
        self.assertIn("WARN service=duosubs component=duosubs event=input_missing", self.output())

    async def test_missing_secondary_keeps_the_response_contract(self):
        with tempfile.TemporaryDirectory() as directory:
            primary = pathlib.Path(directory, "primary.ass")
            primary.write_text("[Script Info]\n")
            result = await service.merge_subtitles(
                self.merge_request(primary_path=str(primary), host_prefix=directory)
            )
        self.assertFalse(result.success)
        self.assertTrue(result.error.startswith("Secondary subtitle not found: "))
        self.assertIn("event=input_missing", self.output())

    async def test_failure_keeps_the_original_error_and_logs_once(self):
        module = service
        real = module.load_subtitles
        module.load_subtitles = lambda _args: (_ for _ in ()).throw(RuntimeError("REVIEW_ONLY_REASON"))
        stdout = io.StringIO()
        try:
            with tempfile.TemporaryDirectory() as directory:
                primary = pathlib.Path(directory, "primary.ass")
                secondary = pathlib.Path(directory, "secondary.ass")
                primary.write_text("[Script Info]\n")
                secondary.write_text("[Script Info]\n")
                with contextlib.redirect_stdout(stdout):
                    result = await module.merge_subtitles(
                        self.merge_request(
                            primary_path=str(primary),
                            secondary_path=str(secondary),
                            output_dir=directory,
                            host_prefix=directory,
                        )
                    )
        finally:
            module.load_subtitles = real

        self.assertEqual(stdout.getvalue(), "", "logging must not bypass the unified stream")
        self.assertFalse(result.success)
        self.assertIn("REVIEW_ONLY_REASON", result.error, "the caller keeps the real reason")
        output = self.output()
        self.assertIn("ERROR service=duosubs component=duosubs event=merge_failed", output)
        self.assertEqual(output.count("event=merge_failed"), 1)
        for line in output.strip().splitlines():
            self.assertEqual(line.count("msg="), 1, line)

    async def test_memory_diagnostics_stay_off_by_default(self):
        self.assertFalse(service._MEMDEBUG_ENABLED)
        service._memory_snapshot("startup_after_model_load", 0)
        self.assertEqual(self.output(), "")


if __name__ == "__main__":
    unittest.main()

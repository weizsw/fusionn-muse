#!/usr/bin/env python3
"""Tests for the shared logfmt renderer."""

import datetime
import io
import logging
import os
import pathlib
import unittest
import warnings
from unittest import mock

import logfmt

# The module's own name, so a copy under another file name does not have to edit
# the assertions below.
MODULE_FILE = pathlib.Path(__file__).name

FIXED_MOMENT = datetime.datetime(
    2026, 7, 1, 14, 20, 3, 456000, tzinfo=datetime.timezone(datetime.timedelta(hours=8))
)

# The same value matrix is rendered byte for byte by the Go services (which use
# strconv.Quote) and by scripts/torrent_complete.py. Verify the Go claim with:
#   fmt.Printf("%s\n", strconv.Quote(value))
SHARED_VALUE_MATRIX = [
    ("abc", "abc"),
    ("SNOS-411", "SNOS-411"),
    ("", '""'),
    ("My Show S01E02.mkv", '"My Show S01E02.mkv"'),
    ("a=b", '"a=b"'),
    ("/data/input/x.mkv", '"/data/input/x.mkv"'),
    ("中文字幕", '"中文字幕"'),
    ('he said "hi"', '"he said \\"hi\\""'),
    ("a\\b", '"a\\\\b"'),
    ("line\nnext", '"line\\nnext"'),
    ("tab\there", '"tab\\there"'),
    ("cr\rhere", '"cr\\rhere"'),
    ("bell\x07", '"bell\\a"'),
    ("vtab\x0b", '"vtab\\v"'),
    ("ff\x0c", '"ff\\f"'),
    ("bs\x08", '"bs\\b"'),
    ("soh\x01", '"soh\\x01"'),
    ("del\x7f", '"del\\x7f"'),
    ("c1\u0080", '"c1\\u0080"'),
    ("c1\u0085", '"c1\\u0085"'),
    ("c1\u009f", '"c1\\u009f"'),
    ("nbsp\u00a0", '"nbsp\\u00a0"'),
    ("shy\u00ad", '"shy\\u00ad"'),
    ("ls\u2028", '"ls\\u2028"'),
    ("ps\u2029", '"ps\\u2029"'),
    ("zwsp\u200b", '"zwsp\\u200b"'),
    ("lrm\u200e", '"lrm\\u200e"'),
    ("rlo\u202e", '"rlo\\u202e"'),
    ("wj\u2060", '"wj\\u2060"'),
    ("bom\ufeff", '"bom\\ufeff"'),
    ("ideo\u3000", '"ideo\\u3000"'),
    ("copy\u00a9", '"copy\u00a9"'),
    ("heart\u2764\ufe0f", '"heart\u2764\ufe0f"'),
    ("keycap1\u20e3", '"keycap1\u20e3"'),
    # A zero-width joiner is a format character, so it is escaped.
    ("family\U0001F468\u200d\U0001F469\u200d\U0001F467", '"family\U0001F468\\u200d\U0001F469\\u200d\U0001F467"'),
    ("emoji\U0001F600", '"emoji\U0001F600"'),
    (200, "200"),
    (True, "true"),
    (False, "false"),
]


def fixed_line(**kwargs):
    kwargs.setdefault("moment", FIXED_MOMENT)
    return logfmt.format_line(**kwargs)


class GoldenVectorTest(unittest.TestCase):
    """The same inputs are rendered byte for byte by the Go services and the shell."""

    def test_correlation_fields_keep_their_order_and_msg_is_last(self):
        line = fixed_line(
            level=logging.WARNING,
            component="processor",
            event="translation_retry",
            msg="Translation will retry",
            fields={
                "retry": 1,
                "error": "provider timeout",
                "retry_after_ms": 2000,
                "stage": "translating",
                "stream": "stderr",
                "note": "a=b",
                "tool": "llm-subtrans",
                "request_id": "req-9",
                "attempt_id": 42,
                "submission_id": "sub-1",
                "job_id": "abc",
            },
        )
        self.assertEqual(
            line,
            "2026-07-01T14:20:03.456+08:00 WARN component=processor "
            "event=translation_retry job_id=abc submission_id=sub-1 attempt_id=42 "
            "request_id=req-9 stage=translating tool=llm-subtrans stream=stderr "
            'error="provider timeout" note="a=b" retry=1 retry_after_ms=2000 '
            'msg="Translation will retry"',
        )

    def test_shared_value_matrix(self):
        for value, expected in SHARED_VALUE_MATRIX:
            with self.subTest(value=value):
                self.assertEqual(logfmt.format_value(value), expected)

    def test_exactly_one_physical_line(self):
        line = fixed_line(
            level=logging.ERROR,
            component="executor",
            event="tool_failed",
            msg="she said \"hi\"\nsecond line",
            fields={"error": "line1\nline2"},
        )
        self.assertNotIn("\n", line)
        self.assertIn('msg="she said \\"hi\\"\\nsecond line"', line)
        self.assertIn('error="line1\\nline2"', line)

    def test_every_line_carries_the_required_fields(self):
        line = fixed_line(level=logging.INFO, component="queue", event="job_started", msg="Job started")
        for required in ("component=queue", "event=job_started", "msg="):
            self.assertIn(required, line)
        self.assertNotIn("service=", line, "the service name is no longer rendered")

    def test_timestamp_is_fixed_utc8_regardless_of_process_tz(self):
        with mock.patch.dict(os.environ, {"TZ": "America/New_York"}):
            self.assertEqual(logfmt.format_timestamp(FIXED_MOMENT), "2026-07-01T14:20:03.456+08:00")
        utc_moment = FIXED_MOMENT.astimezone(datetime.timezone.utc)
        self.assertEqual(logfmt.format_timestamp(utc_moment), "2026-07-01T14:20:03.456+08:00")

    def test_absent_correlation_fields_are_omitted(self):
        line = fixed_line(level=logging.INFO, component="queue", event="job_started", msg="Job started")
        for absent in ("job_id=", "attempt_id=", "request_id=", "stream="):
            self.assertNotIn(absent, line)


class SafetyTest(unittest.TestCase):
    """Redaction, preview limits and field names are enforced in the renderer."""

    SECRET = "REVIEW_ONLY_FAKE_SECRET"

    def test_credentials_are_redacted(self):
        cases = [
            f"Authorization: Bearer {self.SECRET}",
            f"Authorization Bearer {self.SECRET}",
            f"Authorization: Basic {self.SECRET}",
            f"api_key={self.SECRET}",
            f"api-key: {self.SECRET}",
            f"access_key={self.SECRET}",
            f"client_secret={self.SECRET}",
            f"password={self.SECRET}",
            f"token={self.SECRET}",
            f"--api-key {self.SECRET}",
            f"--api-key={self.SECRET}",
            f'--api-key "prefix {self.SECRET}"',
            f"https://user:{self.SECRET}@example.test/v1",
            f"https://{self.SECRET}@example.test/v1",
            f"https://example.test/v1?api_key={self.SECRET}",
            f"https://service.invalid/path?key={self.SECRET}",
            f'Get "https://service.invalid/?key={self.SECRET}": timeout',
            f'{{"api_key":"{self.SECRET}"}}',
            f"upstream rejected sk-{self.SECRET}",
        ]
        for text in cases:
            with self.subTest(text=text):
                rendered = logfmt.format_value(text)
                self.assertNotIn(self.SECRET, rendered)
                self.assertIn("***", rendered)

    def test_credentials_are_redacted_in_message_and_fields(self):
        placements = [
            {"api_key": self.SECRET},
            {"token": self.SECRET},
            {"authorization": self.SECRET},
            {"password": self.SECRET},
            {"client_secret": self.SECRET},
        ]
        for fields in placements:
            for level in (logging.DEBUG, logging.INFO, logging.WARNING, logging.ERROR):
                with self.subTest(level=level, fields=fields):
                    line = fixed_line(level=level, component="c", event="e", msg="safe", fields=fields)
                    self.assertNotIn(self.SECRET, line)

    def test_a_credential_field_is_masked_by_name(self):
        line = fixed_line(
            level=logging.INFO, component="c", event="e", msg="safe", fields={"api_key": self.SECRET}
        )
        self.assertIn('api_key="***"', line)

    def test_the_shared_redaction_vectors(self):
        """The exact output both languages agree on, not just the absence of the secret."""
        vectors = [
            (f"Authorization: Basic {self.SECRET}", '"Authorization: ***"'),
            (f"Authorization: Bearer {self.SECRET}", '"Authorization: ***"'),
            (f"Authorization Bearer {self.SECRET}", '"Authorization ***"'),
            (f"https://user:{self.SECRET}@example.test/v1", '"https://***@example.test/v1"'),
            (f"https://{self.SECRET}@example.test/v1", '"https://***@example.test/v1"'),
            (f"access_key={self.SECRET}", '"access_key=***"'),
            (f"api_key={self.SECRET}", '"api_key=***"'),
            (f"apikey={self.SECRET}", '"apikey=***"'),
            (f"api_secret={self.SECRET}", '"api_secret=***"'),
            (f"auth={self.SECRET}", '"auth=***"'),
            (f"credential={self.SECRET}", '"credential=***"'),
            (f"credentials={self.SECRET}", '"credentials=***"'),
            (f"api-key={self.SECRET}", '"api-key=***"'),
            (f"refresh_token={self.SECRET}", '"refresh_token=***"'),
            (f"db_password={self.SECRET}", '"db_password=***"'),
            (f"proxy_authorization={self.SECRET}", '"proxy_authorization=***"'),
            (f'{{"credential":"{self.SECRET}"}}', '"{\\"credential\\":***}"'),
            (f"https://h/path?credential={self.SECRET}", '"https://h/path?credential=***"'),
            (f"--credential {self.SECRET}", '"--credential ***"'),
        ]
        for text, expected in vectors:
            with self.subTest(text=text):
                self.assertEqual(logfmt.format_value(text), expected)
        trailing = f'Get "https://service.invalid/?key={self.SECRET}": timeout'
        self.assertEqual(
            logfmt.format_value(trailing),
            '"Get \\"https://service.invalid/?key=***\\": timeout"',
            "the closing quote and colon must survive",
        )

    def test_the_shared_credential_field_names(self):
        for field in (
            "api_key",
            "apikey",
            "api_secret",
            "access_key",
            "auth",
            "authorization",
            "client_secret",
            "credential",
            "credentials",
            "password",
            "passwd",
            "secret",
            "token",
        ):
            with self.subTest(field=field):
                line = fixed_line(level=logging.INFO, component="c", event="e", msg="safe", fields={field: self.SECRET})
                self.assertNotIn(self.SECRET, line)

    def test_a_credential_field_is_masked_by_a_name_suffix(self):
        for field in ("refresh_token", "db_password", "db_passwd", "signing_secret", "proxy_authorization"):
            for level in (logging.DEBUG, logging.INFO, logging.ERROR):
                with self.subTest(field=field, level=level):
                    line = fixed_line(level=level, component="c", event="e", msg="safe", fields={field: self.SECRET})
                    self.assertNotIn(self.SECRET, line)
                    self.assertIn(f'{field}="***"', line)

    def test_preview_is_redacted_before_truncation(self):
        padding = "x" * (logfmt.PREVIEW_LIMIT - 10)
        line = fixed_line(
            level=logging.DEBUG,
            component="c",
            event="e",
            msg="m",
            fields={"preview": f"{padding} api_key={self.SECRET}"},
        )
        self.assertNotIn(self.SECRET, line)
        self.assertNotIn("REVIEW", line)

    def test_redaction_reaches_the_whole_line(self):
        line = fixed_line(
            level=logging.ERROR,
            component="executor",
            event="tool_failed",
            msg=f"request failed for Authorization: Bearer {self.SECRET}",
            fields={"error": f"api_key={self.SECRET}", "preview": f"token={self.SECRET}"},
        )
        self.assertNotIn(self.SECRET, line)

    def test_ordinary_text_is_not_redacted(self):
        for text in (
            "provider timeout",
            "monkey business",
            "tokenization",
            "keyboard layout",
            "api gateway",
            "refresh completed",
            "credentials accepted by policy docs",
        ):
            with self.subTest(text=text):
                self.assertIn(text, logfmt.format_value(text))

    def test_preview_is_dropped_above_debug(self):
        line = fixed_line(level=logging.INFO, component="c", event="e", msg="m", fields={"preview": "x" * 600})
        self.assertNotIn("preview=", line)
        self.assertNotIn("x" * 10, line)

    def test_preview_is_bounded_at_debug(self):
        line = fixed_line(level=logging.DEBUG, component="c", event="e", msg="m", fields={"preview": "x" * 600})
        self.assertIn("truncated=true", line)
        self.assertEqual(len(line.split('preview="', 1)[1].split('"', 1)[0]), logfmt.PREVIEW_LIMIT)

    def test_preview_limit_counts_unicode_characters(self):
        line = fixed_line(level=logging.DEBUG, component="c", event="e", msg="m", fields={"preview": "字" * 600})
        preview = line.split('preview="', 1)[1].split('"', 1)[0]
        self.assertEqual(len(preview), logfmt.PREVIEW_LIMIT)
        self.assertIn("truncated=true", line)

    def test_preview_at_the_limit_is_not_marked(self):
        line = fixed_line(level=logging.DEBUG, component="c", event="e", msg="m", fields={"preview": "x" * 512})
        self.assertNotIn("truncated=true", line)

    def test_any_string_field_is_bounded(self):
        line = fixed_line(
            level=logging.ERROR,
            component="executor",
            event="tool_failed",
            msg="Tool failed",
            fields={"error": "tool noise " * 2000},
        )
        self.assertIn("truncated=true", line)
        self.assertLess(len(line), logfmt.FIELD_LIMIT + 500)

    def test_field_names_cannot_inject_or_override(self):
        line = fixed_line(
            level=logging.INFO,
            component="component",
            event="real_event",
            msg="real message",
            fields={
                "event": "fake_event",
                "msg": "fake message",
                "service": "fake-service",
                "bad name": 1,
                "Bad-Name": 2,
                "ev\nil": 3,
                "ok_name": 4,
            },
        )
        self.assertNotIn("service=", line, "the reserved field cannot be injected")
        self.assertIn("component=component", line)
        self.assertIn("event=real_event", line)
        self.assertIn('msg="real message"', line)
        self.assertNotIn("fake", line)
        self.assertIn("ok_name=4", line)
        self.assertEqual(line.count("\n"), 0)


    def test_request_id_only_accepts_the_shared_rule(self):
        accepted = ["abc-123_XY", "req.1", "A", "a" * 64]
        for value in accepted:
            with self.subTest(value=value):
                self.assertEqual(logfmt.request_id(value), value)

        rejected = [
            "é",
            "中文字幕",
            "bad id",
            "bad\tid",
            "bad\nid",
            "",
            "a" * 65,
            "id@host",
            "id/path",
        ]
        for value in rejected:
            with self.subTest(value=value):
                generated = logfmt.request_id(value)
                self.assertNotEqual(generated, value)
                self.assertEqual(len(generated), 32, "a generated id is a uuid4 hex")
                self.assertRegex(generated, r"\A[0-9a-f]{32}\Z")

    def test_a_generated_request_id_matches_the_shared_rule(self):
        generated = logfmt.request_id(None)
        self.assertRegex(generated, r"\A[A-Za-z0-9._-]{1,64}\Z")

    def test_a_generated_request_id_line_is_unchanged_by_quoting(self):
        line = fixed_line(
            level=logging.INFO,
            component="http",
            event="request_completed",
            msg="Request completed",
            fields={"request_id": logfmt.request_id(None)},
        )
        request_field = line.split(" request_id=", 1)[1].split(" ", 1)[0]
        self.assertRegex(request_field, r"\A[0-9a-f]{32}\Z", "an identifier stays unquoted")


class LevelTest(unittest.TestCase):
    def test_warning_maps_to_warn(self):
        self.assertEqual(logfmt.resolve_level("warning")[0], logging.WARNING)
        self.assertEqual(fixed_line(level=logging.WARNING, component="c", event="e", msg="m").split()[1], "WARN")

    def test_default_is_info(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertEqual(logfmt.resolve_level(), (logging.INFO, None))

    def test_debug_is_opt_in(self):
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "debug"}, clear=True):
            self.assertEqual(logfmt.resolve_level()[0], logging.DEBUG)

    def test_log_level_wins_over_compatibility_alias(self):
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "ERROR", "ALIGN_LOG_LEVEL": "DEBUG"}, clear=True):
            self.assertEqual(logfmt.resolve_level()[0], logging.ERROR)
        with mock.patch.dict(os.environ, {"ALIGN_LOG_LEVEL": "DEBUG"}, clear=True):
            self.assertEqual(logfmt.resolve_level()[0], logging.DEBUG)

    def test_unknown_level_falls_back_but_is_reported(self):
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "LOUD"}, clear=True):
            level, invalid = logfmt.resolve_level()
        self.assertEqual(level, logging.INFO)
        self.assertEqual(invalid, "LOUD")


class ConfigureTest(unittest.TestCase):
    def setUp(self):
        self.root = logging.getLogger()
        self.original_handlers = list(self.root.handlers)
        self.original_level = self.root.level
        self.framework = logging.getLogger("uvicorn.error")
        self.framework_handlers = list(self.framework.handlers)
        self.framework_propagate = self.framework.propagate

    def tearDown(self):
        self.root.handlers[:] = self.original_handlers
        self.root.setLevel(self.original_level)
        self.framework.handlers[:] = self.framework_handlers
        self.framework.propagate = self.framework_propagate

    def test_info_suppresses_debug_and_writes_to_the_given_stream(self):
        stream = io.StringIO()
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "INFO"}, clear=True):
            logger = logfmt.configure(service="fusionn-muse", component="llm-subtrans", stream=stream)
            logger.debug("detail", "Hidden detail", preview="x")
            logger.info("translation_started", "Translation started")
        output = stream.getvalue()
        self.assertNotIn("Hidden detail", output)
        self.assertIn("INFO component=llm-subtrans event=translation_started", output)

    def test_debug_flag_reaches_selected_fields(self):
        stream = io.StringIO()
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "DEBUG"}, clear=True):
            logger = logfmt.configure(service="fusionn-muse", component="llm-subtrans", stream=stream)
            logger.debug("tool_started", "Tool started", tool="llm-subtrans")
        self.assertIn("DEBUG component=llm-subtrans event=tool_started tool=llm-subtrans", stream.getvalue())

    def test_repeated_configure_does_not_duplicate_records(self):
        stream = io.StringIO()
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "INFO"}, clear=True):
            logfmt.configure(service="fusionn-muse", component="llm-subtrans", stream=stream)
            logger = logfmt.configure(service="fusionn-muse", component="llm-subtrans", stream=stream)
            logger.info("translation_started", "Translation started")
        self.assertEqual(stream.getvalue().count("event=translation_started"), 1)
        self.assertEqual(len(self.root.handlers), 1)

    def test_unknown_log_level_is_visible(self):
        stream = io.StringIO()
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "LOUD"}, clear=True):
            logfmt.configure(service="fusionn-muse", component="llm-subtrans", stream=stream)
        output = stream.getvalue()
        self.assertIn("WARN", output)
        self.assertIn("event=config_invalid", output)
        self.assertIn("log_level=LOUD", output)
        self.assertIn('msg="Invalid LOG_LEVEL, using INFO"', output)

    def test_third_party_records_are_not_bypassed(self):
        stream = io.StringIO()
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "INFO"}, clear=True):
            logfmt.configure(service="host-asr", component="host-asr", stream=stream)
            logging.getLogger("uvicorn.error").warning("Started server process [4321]")
        output = stream.getvalue()
        self.assertIn("WARN component=host-asr event=server_started", output)
        self.assertIn("logger=uvicorn.error", output)
        self.assertIn("pid=4321", output)
        self.assertIn('msg="Server process started"', output)

    def test_third_party_content_never_appears_and_info_is_suppressed(self):
        stream = io.StringIO()
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "INFO"}, clear=True):
            logfmt.configure(service="fusionn-muse", component="llm-subtrans", stream=stream)
            logger = logging.getLogger("third-party-translator")
            logger.info("PROMPT_BODY_SHOULD_BE_DEBUG_ONLY")
            logger.error("subtitle body here")
            logger.warning("a prompt and a body")
        output = stream.getvalue()
        for content in ("PROMPT_BODY_SHOULD_BE_DEBUG_ONLY", "subtitle body here", "a prompt and a body"):
            self.assertNotIn(content, output)
        # An unknown record has no message left to show, so INFO adds nothing and
        # is not displayed; WARN and ERROR stay, because they are actionable.
        self.assertEqual(output.count("event=log"), 2)
        self.assertNotIn("INFO", output)

    def test_an_unknown_library_cannot_flood_the_stream(self):
        for configured in ("DEBUG", "INFO"):
            with self.subTest(level=configured):
                stream = io.StringIO()
                with mock.patch.dict(os.environ, {"LOG_LEVEL": configured}, clear=True):
                    logfmt.configure(service="fusionn-muse", component="llm-subtrans", stream=stream)
                    logger = logging.getLogger("third-party-translator")
                    for index in range(200):
                        logger.info("noise %d", index)
                        logger.debug("noise %d", index)
                self.assertEqual(stream.getvalue(), "")

    def test_third_party_exception_text_is_not_emitted(self):
        stream = io.StringIO()
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "INFO"}, clear=True):
            logfmt.configure(service="fusionn-muse", component="llm-subtrans", stream=stream)
            try:
                raise ValueError("REVIEW_ONLY_SUBTITLE_TEXT")
            except ValueError:
                logging.getLogger("PySubtrans").error("translation failed", exc_info=True)
        output = stream.getvalue()
        self.assertNotIn("REVIEW_ONLY_SUBTITLE_TEXT", output)
        self.assertIn("error_type=ValueError", output)

    def test_python_warnings_use_the_reviewed_format(self):
        """A warning must not reach stderr directly: it is one reviewed line."""
        canary = "WARNING_BODY_REVIEW_ONLY_CANARY"
        stderr = io.StringIO()
        stream = io.StringIO()
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "INFO"}, clear=True), mock.patch("sys.stderr", stderr):
            with warnings.catch_warnings():
                warnings.simplefilter("always")
                logfmt.configure(service="fusionn-muse", component="worker", stream=stream)
                warnings.warn(canary)
        output = stream.getvalue()
        self.assertIn(" WARN ", output)
        self.assertIn("logger=py.warnings", output)
        self.assertIn('msg="Third-party log"', output)
        self.assertNotIn(canary, output)
        self.assertNotIn(canary, stderr.getvalue())

    def test_third_party_exception_keeps_safe_frames(self):
        """The type alone would lose the location; the frames keep it, without a source line."""
        stream = io.StringIO()
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "INFO"}, clear=True):
            logfmt.configure(service="fusionn-muse", component="llm-subtrans", stream=stream)
            try:
                raise ValueError("REVIEW_ONLY_SUBTITLE_TEXT")
            except ValueError:
                logging.getLogger("uvicorn.error").error("Exception in ASGI application", exc_info=True)
        output = stream.getvalue()
        self.assertIn("error_type=ValueError", output)
        self.assertIn("stack=", output)
        self.assertIn("test_third_party_exception_keeps_safe_frames", output)
        self.assertNotIn("REVIEW_ONLY_SUBTITLE_TEXT", output)

    def test_a_credential_straddling_the_field_limit_is_still_redacted(self):
        """The whole value is redacted before the cut, so a split credential cannot survive."""
        canary = "PW_REVIEW_ONLY_CANARY"
        raw = "x" * 4050 + " https://user:" + canary + "z" * 100 + "@host.invalid/path" + "y" * 2000
        line = logfmt.format_line(logging.ERROR, "third_party_failed", "The tool failed", {"detail": raw})
        self.assertNotIn(canary, line)
        self.assertIn("truncated=true", line)
        self.assertLessEqual(len(line), logfmt.FIELD_LIMIT + 500)
        # The same value without the credential still loses its tail at the limit.
        plain = logfmt.format_line(logging.ERROR, "third_party_failed", "The tool failed", {"detail": "y" * 5000})
        self.assertIn("truncated=true", plain)
        self.assertNotIn("y" * 4097, plain)

    def test_one_name_set_covers_fields_text_and_flags(self):
        secret = "REVIEW_ONLY_VALUE"
        for name in (
            "api_key",
            "api_secret",
            "db_password",
            "access_token",
            "refresh_token",
            "foo_api_key_value",
            "openrouter_api_key",
            "my_db_password",
        ):
            # A hyphen is not a legal field name, so only the underscore spelling
            # reaches a rendered field.
            with self.subTest(name=name):
                line = logfmt.format_line(logging.INFO, "config_loaded", "Configuration loaded", {name: secret})
                self.assertIn('%s="***"' % name, line)
                self.assertNotIn(secret, line)

        # The same names in free text and in a command line, hyphen and
        # underscore spellings alike.
        for phrase in (
            "provider=openrouter_api_key=" + secret,
            "provider=openrouter-api-key=" + secret,
            "run --openrouter_api_key=" + secret,
            "run --openrouter-api-key " + secret,
            "run --my-db-password " + secret,
            "run --my_db_password=" + secret,
            "run --refresh-token " + secret,
            "run --proxy_authorization " + secret,
        ):
            with self.subTest(phrase=phrase):
                line = logfmt.format_line(logging.INFO, "tool_started", "Tool started", {"detail": phrase})
                self.assertNotIn(secret, line)

        # Ordinary words that merely look similar stay readable.
        for phrase in ("count --token-count 5", "tokenizer=fast", "provider timeout", "api gateway"):
            with self.subTest(phrase=phrase):
                line = logfmt.format_line(logging.INFO, "tool_started", "Tool started", {"detail": phrase})
                self.assertIn(phrase, line)

    def test_third_party_records_never_carry_the_message(self):
        for configured in ("DEBUG", "INFO", "ERROR"):
            with self.subTest(level=configured):
                stream = io.StringIO()
                with mock.patch.dict(os.environ, {"LOG_LEVEL": configured}, clear=True):
                    logfmt.configure(service="fusionn-muse", component="llm-subtrans", stream=stream)
                    logger = logging.getLogger("PySubtrans.Translator")
                    logger.debug("DEBUG_BODY_TEXT")
                    logger.info("INFO_BODY_TEXT")
                    logger.error("ERROR_BODY_TEXT")
                output = stream.getvalue()
                for content in ("DEBUG_BODY_TEXT", "INFO_BODY_TEXT", "ERROR_BODY_TEXT"):
                    self.assertNotIn(content, output)
                self.assertNotIn("preview=", output, "an excerpt is only ever chosen deliberately")

    def test_an_unmatched_third_party_error_is_not_a_failure(self):
        stream = io.StringIO()
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "INFO"}, clear=True):
            logfmt.configure(service="fusionn-muse", component="llm-subtrans", stream=stream)
            logging.getLogger("PySubtrans").error("harmless progress note")
        self.assertNotIn("marker=", stream.getvalue())

    def test_a_framework_logger_that_configured_first_is_adopted(self):
        """uvicorn installs its own handler with propagate=False before configure()."""
        own = io.StringIO()
        self.framework.addHandler(logging.StreamHandler(own))
        self.framework.propagate = False

        stream = io.StringIO()
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "INFO"}, clear=True):
            logfmt.configure(service="host-asr", component="host-asr", stream=stream)
            self.framework.warning("Started server process")

        self.assertEqual(own.getvalue(), "", "the framework handler must be detached")
        self.assertIn("logger=uvicorn.error", stream.getvalue())

    def test_an_exception_never_carries_its_message(self):
        canary = "MODEL_RESPONSE_BODY_NEVER_INFO " + "字幕原文" * 150
        for configured in ("INFO", "WARN", "ERROR"):
            with self.subTest(level=configured):
                stream = io.StringIO()
                with mock.patch.dict(os.environ, {"LOG_LEVEL": configured}, clear=True):
                    logger = logfmt.configure(service="fusionn-muse", component="detect-hard-sub", stream=stream)
                    try:
                        raise RuntimeError(canary)
                    except RuntimeError:
                        logger.exception("ocr_failed", "Hard-sub OCR failed")
                output = stream.getvalue()
                self.assertEqual(output.count("\n"), 1)
                self.assertIn("ERROR", output)
                self.assertNotIn("MODEL_RESPONSE_BODY_NEVER_INFO", output)
                self.assertNotIn("字幕原文", output)
                self.assertIn('error="RuntimeError"', output)
                self.assertIn("stack=", output, "the call frames are still reported")
                self.assertIn(MODULE_FILE, output)

    def test_a_long_frame_chain_is_bounded(self):
        def recurse(depth):
            if depth == 0:
                raise ValueError("deep")
            return recurse(depth - 1)

        stream = io.StringIO()
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "INFO"}, clear=True):
            logger = logfmt.configure(service="fusionn-muse", component="detect-hard-sub", stream=stream)
            try:
                recurse(400)
            except ValueError:
                logger.exception("ocr_failed", "Hard-sub OCR failed")
        output = stream.getvalue()
        self.assertIn("truncated=true", output)
        self.assertLess(len(output), logfmt._STACK_LIMIT + 1000)
        self.assertIn("in recurse", output)

    def test_a_deliberate_debug_exception_is_one_bounded_excerpt(self):
        canary = "MODEL_RESPONSE_BODY_NEVER_INFO " + "字幕原文" * 150
        stream = io.StringIO()
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "DEBUG"}, clear=True):
            logfmt.configure(service="fusionn-muse", component="llm-subtrans", stream=stream)
            try:
                raise RuntimeError(canary)
            except RuntimeError:
                logging.getLogger("fusionn-muse").debug(
                    "provider response",
                    exc_info=True,
                    extra={
                        "event": "provider_response",
                        "fields": {},
                        "component": "llm-subtrans",
                        "service": "fusionn-muse",
                    },
                )
        output = stream.getvalue()
        self.assertEqual(output.count("preview="), 1)
        self.assertEqual(len(output.split('preview="', 1)[1].split('"', 1)[0]), logfmt.PREVIEW_LIMIT)
        self.assertIn("truncated=true", output)
        self.assertNotIn(canary, output)

    def test_an_exception_message_cannot_reach_the_event_through_a_trusted_type(self):
        # An OSError carries whatever text it was raised with. The reason must come
        # from the operating system's table for the code, not from that text.
        canary = "MODEL_RESPONSE_BODY_NEVER_INFO"
        for configured in ("DEBUG", "INFO", "WARN", "ERROR"):
            with self.subTest(level=configured):
                stream = io.StringIO()
                with mock.patch.dict(os.environ, {"LOG_LEVEL": configured}, clear=True):
                    logger = logfmt.configure(service="fusionn-muse", component="llm-subtrans", stream=stream)
                    try:
                        raise OSError(5, canary)
                    except OSError:
                        logger.exception("translation_failed", "Subtitle translation failed")
                output = stream.getvalue()
                self.assertNotIn(canary, output)
                self.assertIn("errno=5", output)
                self.assertIn('error="OSError"', output)
                self.assertIn("stack=", output, "the call frames are still reported")

    def test_an_unknown_errno_still_reports_the_code(self):
        stream = io.StringIO()
        with mock.patch.dict(os.environ, {"LOG_LEVEL": "INFO"}, clear=True):
            logger = logfmt.configure(service="fusionn-muse", component="llm-subtrans", stream=stream)
            try:
                raise OSError(987654, "MODEL_RESPONSE_BODY_NEVER_INFO")
            except OSError:
                logger.exception("translation_failed", "Subtitle translation failed")
        output = stream.getvalue()
        self.assertIn("errno=987654", output)
        self.assertNotIn("MODEL_RESPONSE_BODY_NEVER_INFO", output)

    def test_a_trusted_exception_keeps_its_structural_facts(self):
        cases = [
            (
                OSError(2, "No such file or directory"),
                ['errno=2', 'error="FileNotFoundError"', 'strerror="No such file or directory"'],
            ),
            (
                ModuleNotFoundError("No module named 'PySubtrans'", name="PySubtrans"),
                ["module=PySubtrans", 'error="ModuleNotFoundError"'],
            ),
        ]
        for exc, expected in cases:
            with self.subTest(exc=type(exc).__name__):
                stream = io.StringIO()
                with mock.patch.dict(os.environ, {"LOG_LEVEL": "INFO"}, clear=True):
                    logger = logfmt.configure(service="fusionn-muse", component="llm-subtrans", stream=stream)
                    try:
                        raise exc
                    except Exception:
                        logger.exception("failed", "Operation failed")
                output = stream.getvalue()
                for item in expected:
                    self.assertIn(item, output)
                self.assertNotIn("No module named", output, "the message itself is not emitted")


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Unified single-line log format for fusionn-muse scripts and host services.

Timestamp, field order, quoting and level names are shared with the Go services in
this repository. See docs/design/unified-logging.md.

Import style depends on the entry point:

* ``scripts/*.py``, which run with ``scripts/`` on ``sys.path`` (container image,
  ``unittest discover -s scripts``), import this module as ``logfmt``.
* ``host_*.py``, which run from the repository root, import it as
  ``scripts.logfmt`` (a namespace package).

Every process writes logs to stderr so stdout stays machine readable; callers such
as ``detect_hard_sub.py`` print their result on stdout.

Content rules enforced here, for every record from any logger:

* Credentials are redacted before rendering (``format_value``), so no caller can
  emit a key, token, password, Authorization header or URL userinfo.
* ``preview`` is DEBUG only and is cut to ``PREVIEW_LIMIT`` Unicode characters.
* A third-party record has no domain event, so its message is untrusted content:
  it is bounded and marked ``truncated``.
* Field names must be lowercase snake_case and cannot be ``service``,
  ``component``, ``event``, ``msg``, ``level`` or ``time``, so a value can never
  forge the fields that structure a line. Withheld fields are counted in
  ``dropped_fields``.

Because the formatter already contains every record, a parent process may pass
through child output that matches the unified line shape: it is normalized,
redacted and bounded when it is produced. The line shape is a format check, not a
content-safety boundary.
"""

from __future__ import annotations

import datetime
import logging
import os
import re
import sys
import uuid

__all__ = [
    "SERVICE",
    "PREVIEW_LIMIT",
    "FIELD_LIMIT",
    "Logger",
    "configure",
    "format_line",
    "format_timestamp",
    "format_value",
    "request_id",
    "request_summary",
    "resolve_level",
]

SERVICE = "fusionn-muse"

# A log timestamp is always UTC+8. The process TZ is never modified, so business
# timing and quota resets keep using the host timezone.
_UTC8 = datetime.timezone(datetime.timedelta(hours=8))

# Correlation fields keep this order; every other field is sorted by name and
# ``msg`` stays last.
_CORRELATION = (
    "job_id",
    "submission_id",
    "attempt_id",
    "request_id",
    "parent_job_id",
    "stage",
    "tool",
    "stream",
)

# Only identifier-shaped values are safe to emit unquoted.
_IDENTIFIER = re.compile(r"\A[A-Za-z0-9][A-Za-z0-9_.-]*\Z")

# Field names are lowercase snake_case; these names belong to the line itself.
_FIELD_NAME = re.compile(r"\A[a-z][a-z0-9_]*\Z")
_RESERVED = frozenset({"service", "component", "event", "msg", "level", "time", "timestamp"})

# A DEBUG content snippet, in Unicode characters.
PREVIEW_LIMIT = 512
# Any single string field is bounded, so tool or upstream output cannot fill a line.
FIELD_LIMIT = 4096
# No single string field may carry unbounded tool or upstream output.
_FIELD_LIMIT = FIELD_LIMIT

_LEVEL_NAMES = {
    logging.DEBUG: "DEBUG",
    logging.INFO: "INFO",
    logging.WARNING: "WARN",
    logging.ERROR: "ERROR",
    logging.CRITICAL: "ERROR",
}

_ENV_LEVELS = {
    "DEBUG": logging.DEBUG,
    "INFO": logging.INFO,
    "WARN": logging.WARNING,
    "WARNING": logging.WARNING,
    "ERROR": logging.ERROR,
}

_ERROR_LIMIT = 1024

_STACK_LIMIT = 4096


def _frame_lines(traceback_object):
    """The standard frame format, without the source line or the message.

    ``traceback.StackSummary.format()`` re-reads the source line of every frame
    even with ``lookup_lines=False``, and a frame's source line can itself
    contain the failing value. Frames therefore carry only where the failure
    happened: file, line and function.
    """
    lines = []
    while traceback_object is not None:
        code = traceback_object.tb_frame.f_code
        lines.append('  File "%s", line %d, in %s\n' % (code.co_filename, traceback_object.tb_lineno, code.co_name))
        traceback_object = traceback_object.tb_next
    return "".join(lines)


def _safe_exception_frame_fields(traceback_object):
    """Where a failure happened: frames without the source line or the message."""
    frames, frames_bounded = _bounded(_frame_lines(traceback_object), _STACK_LIMIT)
    fields = {"stack": frames}
    if frames_bounded:
        fields["truncated"] = True
    return fields


def _safe_exception_fields(value):
    """Facts about an exception that are structural rather than content."""
    fields = {}
    if isinstance(value, OSError):
        if value.errno is not None:
            fields["errno"] = value.errno
            # The reason comes from the operating system's table for that code,
            # never from the exception's own text, which can carry any content.
            try:
                reason = os.strerror(value.errno)
            except (ValueError, OverflowError):
                reason = ""
            if reason:
                fields["strerror"] = reason
    if isinstance(value, ImportError) and value.name:
        fields["module"] = value.name
    return fields


# A third-party record carries content we did not author, so it never emits its
# original message: only constant metadata, or whitelisted safe fields from a
# known framework record. A deliberate 512-character excerpt belongs at a chosen
# failure boundary, where our own code passes the ``preview`` field itself.
# Framework lifecycle records whose whole shape is known, so their fields are
# safe without carrying any free text. (pattern, event, message, field name)
_SAFE_RECORDS = (
    (re.compile(r"\AStarted server process \[(\d+)\]\Z"), "server_started", "Server process started", "pid"),
    (re.compile(r"\AWaiting for application startup\.\Z"), "startup_waiting", "Waiting for application startup", None),
    (re.compile(r"\AApplication startup complete\.\Z"), "startup_complete", "Application startup complete", None),
    (
        re.compile(r"\AUvicorn running on (\S+) \(Press CTRL\+C to quit\)\Z"),
        "server_listening",
        "Server listening",
        "url",
    ),
    (re.compile(r"\AApplication shutdown complete\.\Z"), "shutdown_complete", "Application shutdown complete", None),
    (re.compile(r"\AFinished server process \[(\d+)\]\Z"), "server_stopped", "Server process stopped", "pid"),
    (re.compile(r"\AShutting down\Z"), "server_shutting_down", "Server shutting down", None),
)

# A credential field is masked by name, so its value never reaches the line even
# when the value has no recognizable shape.
_SECRET_FIELD_NAMES = frozenset(
    {
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
    }
)
_SECRET_FIELD_SUFFIXES = ("_token", "_password", "_passwd", "_secret", "_authorization")


def _name_alternation():
    """The credential names as a regex alternation, longest first.

    ``api_key`` and ``api-key`` both match. A suffix form such as
    ``refresh_token`` is covered by the trailing alternative, which is built from
    the same suffix list the field-name predicate uses.
    """
    named = "|".join(
        name.replace("_", "[_-]") for name in sorted(_SECRET_FIELD_NAMES, key=len, reverse=True)
    )
    suffixed = "|".join(suffix.lstrip("_").replace("_", "[_-]") for suffix in _SECRET_FIELD_SUFFIXES)
    # A candidate key is made of key characters, hyphens and underscores
    # included, so a prefixed name keeps its shape: --my-db-password VALUE and
    # --openrouter-api-key VALUE are the same names as their underscore forms.
    key_chars = "A-Za-z0-9_-"
    # A name that merely contains api_key, such as openrouter_api_key, is a
    # credential for the field-name masker, so the text patterns cover it too.
    containing = r"[%s]*api[_-]key[%s]*" % (key_chars, key_chars)
    return r"%s|[%s]+[_-](?:%s)|%s" % (named, key_chars, suffixed, containing)


# The field names, the text patterns and the command-line patterns all use this
# one name set, so a name cannot be recognised in one place and missed in the
# other.
_SECRET_TEXT = _name_alternation()

_MESSAGE_LOGGER = "fusionn-muse"
_state = {"service": SERVICE, "component": SERVICE, "level": logging.INFO, "configured": None}

# Redaction runs before quoting and before truncation, on every value, so a
# credential cannot survive as a left-over fragment. Order matters: URL userinfo,
# query parameters, command-line options and authorization schemes first, then
# key/value credentials, so a redacted value cannot be re-redacted into a leak.
_REDACTIONS = (
    # The whole userinfo segment, with or without a password.
    (re.compile(r"(?<=://)[^/@\s]+(?=@)"), "***"),
    (
        re.compile(
            r"(?i)([?&](?:%s|key|signature|sig)=)[^&\s\"']+" % _SECRET_TEXT
        ),
        r"\1***",
    ),
    (
        re.compile(
            r"""(?i)(--(?:%s|key)(?:=|\s+))(?:"[^"]*"|'[^']*'|\S+)""" % _SECRET_TEXT
        ),
        r"\1***",
    ),
    (re.compile(r"(?i)\b(basic|bearer|digest)\s+\S+"), "***"),
    (
        re.compile(
            r"(?i)([\"']?\b(?:%s)\b[\"']?\s*[:=]\s*)" % _SECRET_TEXT
            + r"(?:\"[^\"]*\"|'[^']*'|[^\s,;\"']+)"
        ),
        r"\1***",
    ),
    (re.compile(r"\bsk-[A-Za-z0-9_-]{8,}"), "sk-***"),
    (re.compile(r"\bAIza[A-Za-z0-9_-]{10,}"), "AIza***"),
)

# strconv.Quote uses these names for the C0 characters with a short escape.
_SHORT_ESCAPES = {
    "\\": "\\\\",
    '"': '\\"',
    "\a": "\\a",
    "\b": "\\b",
    "\f": "\\f",
    "\n": "\\n",
    "\r": "\\r",
    "\t": "\\t",
    "\v": "\\v",
}


def _redact(text):
    for pattern, replacement in _REDACTIONS:
        text = pattern.sub(replacement, text)
    return text


def _escape(text):
    """Quote one value exactly like Go's strconv.Quote.

    Printable text, including non-ASCII, stays literal; everything else gets a
    named escape, ``\\xNN``, ``\\uNNNN`` or ``\\UNNNNNNNN``. Python's
    ``str.isprintable`` and Go's ``unicode.IsPrint`` exclude the same categories.
    """
    escaped = []
    for char in text:
        if char in _SHORT_ESCAPES:
            escaped.append(_SHORT_ESCAPES[char])
        elif char.isprintable():
            escaped.append(char)
        elif char < "\x80":
            escaped.append("\\x%02x" % ord(char))
        elif ord(char) < 0x10000:
            escaped.append("\\u%04x" % ord(char))
        else:
            escaped.append("\\U%08x" % ord(char))
    return "".join(escaped)


def format_value(value):
    """Render one field value: redacted first, then quoted with the shared rules."""
    if value is True:
        return "true"
    if value is False:
        return "false"
    if isinstance(value, (int, float)):
        return str(value)
    text = _redact(value if isinstance(value, str) else str(value))
    if _IDENTIFIER.match(text):
        return text
    return '"%s"' % _escape(text)


# Free-form content fields are always quoted, whatever their shape: the
# unquoted form is only for identifier-class values.
_ALWAYS_QUOTED = frozenset({"preview", "stack", "error"})


def _render_field(key, value):
    if key in _ALWAYS_QUOTED and not isinstance(value, (bool, int, float)):
        text = _redact(value if isinstance(value, str) else str(value))
        return '"%s"' % _escape(text)
    return format_value(value)


def format_timestamp(moment=None):
    """Render an aware datetime as YYYY-MM-DDTHH:MM:SS.SSS+08:00."""
    moment = datetime.datetime.now(_UTC8) if moment is None else moment.astimezone(_UTC8)
    stamp = moment.strftime("%Y-%m-%dT%H:%M:%S.%f")
    offset = moment.strftime("%z")
    return "%s.%s%s:%s" % (stamp[:19], stamp[20:23], offset[:3], offset[3:])


def _is_secret_field(key):
    """Whether a field name itself implies a credential.

    The same shape the Go renderer uses: an exact name, any name containing
    ``api_key`` (``foo_api_key_value``), or a name ending in a credential suffix.
    Hyphen and underscore spellings are the same name.
    """
    name = key.lower().replace("-", "_")
    if name in _SECRET_FIELD_NAMES or "api_key" in name:
        return True
    return name.endswith(_SECRET_FIELD_SUFFIXES)


def _sanitized(fields):
    """Drop invalid or reserved names, and mask credential fields by name."""
    accepted = {}
    for key, value in (fields or {}).items():
        if value is None:
            continue
        if not isinstance(key, str) or key in _RESERVED or not _FIELD_NAME.match(key):
            continue
        accepted[key] = "***" if _is_secret_field(key) else value
    return accepted


def format_line(level, event, msg, fields=None, component=None, service=None, moment=None):
    """Render one physical log line. Never returns an embedded newline.

    ``preview`` is DEBUG only. Invalid and reserved field names are dropped, a
    field whose name is a credential is masked whatever its value, and every
    other value is redacted before it is cut to ``FIELD_LIMIT`` or
    ``PREVIEW_LIMIT``.
    """
    accepted = _sanitized(fields)
    if "preview" in accepted:
        if level > logging.DEBUG:
            # Content snippets are DEBUG only; INFO and above never carry them.
            del accepted["preview"]
        else:
            text = accepted["preview"]
            text = _redact(text if isinstance(text, str) else str(text))
            if len(text) > PREVIEW_LIMIT:
                accepted["preview"] = text[:PREVIEW_LIMIT]
                accepted["truncated"] = True
            else:
                accepted["preview"] = text

    parts = [
        format_timestamp(moment),
        _LEVEL_NAMES.get(level, "INFO"),
        "component=" + format_value(component or _state["component"]),
        "event=" + format_value(event),
    ]
    for key, value in list(accepted.items()):
        if isinstance(value, str):
            # Redact the whole value before any cut: a credential that straddles
            # the limit would otherwise lose the anchor its pattern needs.
            text = _redact(value)
            if len(text) > _FIELD_LIMIT:
                text = text[:_FIELD_LIMIT]
                accepted["truncated"] = True
            accepted[key] = text

    for key in _CORRELATION:
        if accepted.get(key) is not None:
            parts.append("%s=%s" % (key, format_value(accepted.pop(key))))
    for key in sorted(accepted):
        if accepted[key] is not None:
            parts.append("%s=%s" % (key, _render_field(key, accepted[key])))
    parts.append('msg="%s"' % _escape(_redact(msg)))
    return " ".join(parts)


# Only these bytes are a legal caller request id; Python's str.isalnum would
# accept non-ASCII letters, which the shared rule does not.
_REQUEST_ID = re.compile(r"\A[A-Za-z0-9._-]{1,64}\Z")


def request_id(header=None):
    """Reuse a well-formed caller request id, otherwise generate one.

    The caller's value is used only when it matches the shared rule exactly, the
    same one the Go services apply, so an unexpected value is replaced instead of
    reaching a log line.
    """
    if header and _REQUEST_ID.match(header):
        return header
    return uuid.uuid4().hex


def request_summary(logger, route, method, status, incoming, duration_ms):
    """Emit the one HTTP summary line, at the level the outcome deserves."""
    fields = {
        "request_id": incoming,
        "method": method,
        "route": route,
        "status": status,
        "duration_ms": duration_ms,
    }
    if status >= 500:
        logger.error("request_completed", "Request failed", **fields)
    elif status >= 400:
        logger.warn("request_completed", "Request rejected", **fields)
    elif route == "/health":
        # A successful health check must not add noise at INFO.
        logger.debug("request_completed", "Health check completed", **fields)
    else:
        logger.info("request_completed", "Request completed", **fields)


def resolve_level(level=None):
    """Return (level, invalid_value) for LOG_LEVEL, defaulting to INFO."""
    if level is None:
        raw = os.environ.get("LOG_LEVEL")
        if raw is None or not raw.strip():
            # Compatibility alias, used only when LOG_LEVEL is unset.
            raw = os.environ.get("ALIGN_LOG_LEVEL")
    else:
        raw = level
    if raw is None:
        return logging.INFO, None
    name = raw.strip().upper()
    if name in _ENV_LEVELS:
        return _ENV_LEVELS[name], None
    return logging.INFO, raw


def _bounded(text, limit):
    """Keep both ends so a traceback header and the final reason survive."""
    if len(text) <= limit:
        return text, False
    half = limit // 2
    return text[:half] + "\n...\n" + text[-half:], True


def _known_record(record):
    """The whitelisted rendering of a known framework record, or None."""
    try:
        message = record.getMessage()
    except Exception:
        return None
    for pattern, event, summary, key in _SAFE_RECORDS:
        match = pattern.match(message)
        if match:
            return event, summary, match, key
    return None


class _ThirdPartyNoiseFilter(logging.Filter):
    """Keeps an unknown third-party record below WARN out of the displayed stream.

    Such a record has no domain event and no known shape, so its rendered line is
    a constant that carries no information, and a library that logs on every call
    would fill the stream with it. WARN and ERROR stay, because they are
    actionable, and a known framework record stays, because it carries safe
    fields. An observer such as the translation script's compatibility probe is a
    separate handler, so it still sees every record.
    """

    def filter(self, record):
        if getattr(record, "event", None) is not None:
            return True
        if record.levelno >= logging.WARNING:
            return True
        return _known_record(record) is not None


class _Formatter(logging.Formatter):
    """Renders records from any logger, including third-party ones."""

    def _third_party_fields(self, record):
        """(event, msg, fields) for a record that carries no domain event.

        The original message is content we did not author, so it is never emitted
        automatically. An unknown record contributes its logger name, its level
        and, for an exception, the exception type and where it happened. Known
        framework lifecycle records additionally contribute whitelisted fields.
        """
        fields = {"logger": record.name}
        if record.exc_info:
            kind, value, traceback_object = record.exc_info
            # The type alone would lose the location, so an exception also keeps
            # its structural facts and its call frames, never its text or a source
            # line.
            fields["error_type"] = kind.__name__
            for key, item in _safe_exception_fields(value).items():
                fields.setdefault(key, item)
            fields.update(_safe_exception_frame_fields(traceback_object))
        known = _known_record(record)
        if known is not None:
            event, summary, match, key = known
            if key:
                fields[key] = match.group(1)
            return event, summary, fields
        return "log", "Third-party log", fields

    def format(self, record):
        moment = datetime.datetime.fromtimestamp(record.created, _UTC8)
        component = getattr(record, "component", None)
        service = getattr(record, "service", None)
        event = getattr(record, "event", None)
        if event is None:
            event, msg, fields = self._third_party_fields(record)
            return format_line(
                record.levelno, event, msg, fields, component=component, service=service, moment=moment
            )

        fields = dict(getattr(record, "fields", None) or {})
        if record.exc_info:
            kind, value, traceback_object = record.exc_info
            # The exception text is content we did not author and can hold a model
            # response or a subtitle body, so the event keeps the type, any
            # structural fact, and where it happened. The text itself appears only
            # in a deliberate DEBUG excerpt.
            fields.setdefault("error", kind.__name__)
            for key, item in _safe_exception_fields(value).items():
                fields.setdefault(key, item)
            for key, item in _safe_exception_frame_fields(traceback_object).items():
                fields.setdefault(key, item)
            if record.levelno <= logging.DEBUG:
                fields.setdefault("preview", str(value))
        return format_line(
            record.levelno,
            event,
            record.getMessage(),
            fields,
            component=component,
            service=service,
            moment=moment,
        )


class Logger:
    """Emits unified events for one component of the service."""

    def __init__(self, component, service=SERVICE):
        self.component = component
        self.service = service

    def emit(self, level, event, msg, **fields):
        # The configured level filters here, because the root logger itself stays
        # open so an observer can see every record.
        if level < _state["level"]:
            return
        logging.getLogger(_MESSAGE_LOGGER).log(
            level,
            msg,
            extra={"event": event, "fields": fields, "component": self.component, "service": self.service},
        )

    def debug(self, event, msg, **fields):
        self.emit(logging.DEBUG, event, msg, **fields)

    def info(self, event, msg, **fields):
        self.emit(logging.INFO, event, msg, **fields)

    def warn(self, event, msg, **fields):
        self.emit(logging.WARNING, event, msg, **fields)

    def error(self, event, msg, **fields):
        self.emit(logging.ERROR, event, msg, **fields)

    def exception(self, event, msg, **fields):
        """ERROR event carrying the active exception traceback as a bounded field."""
        if logging.ERROR < _state["level"]:
            return
        logging.getLogger(_MESSAGE_LOGGER).error(
            msg,
            exc_info=True,
            extra={"event": event, "fields": fields, "component": self.component, "service": self.service},
        )


def _adopt_existing_loggers():
    """Detach handlers third-party loggers installed before configure().

    A logger with its own handler and ``propagate = False`` would bypass the
    unified format entirely, so those handlers are removed and the records are
    left to reach the root logger.
    """
    for logger in list(logging.Logger.manager.loggerDict.values()):
        if not isinstance(logger, logging.Logger) or not logger.handlers:
            continue
        for handler in list(logger.handlers):
            logger.removeHandler(handler)
        logger.propagate = True


def configure(service=SERVICE, component=None, level=None, stream=None):
    """Install the unified formatter and return a component logger.

    Call this before third-party imports. Safe to call repeatedly: the previous
    handler is replaced, so one record is never rendered twice. An unknown level
    logs a visible diagnostic and falls back to INFO instead of silently
    discarding logs.
    """
    stream = sys.stderr if stream is None else stream
    component = component or service
    resolved, invalid = resolve_level(level)
    if _state["configured"] == (service, component, stream, resolved):
        return Logger(component, service)
    _state["service"] = service
    _state["component"] = component
    _state["configured"] = (service, component, stream, resolved)

    handler = logging.StreamHandler(stream)
    handler.setFormatter(_Formatter())
    handler.setLevel(resolved)
    handler.addFilter(_ThirdPartyNoiseFilter())
    root = logging.getLogger()
    for existing in list(root.handlers):
        root.removeHandler(existing)
    root.addHandler(handler)
    # The root logger stays open so an observer, such as a script's compatibility
    # probe, can see every record; the configured level filters what is displayed.
    root.setLevel(logging.DEBUG)
    # The warnings module writes to stderr on its own, which would bypass this
    # format and everything it redacts; route it through the py.warnings logger
    # instead, so a warning is one reviewed line like any other record.
    logging.captureWarnings(True)
    _state["level"] = resolved
    _adopt_existing_loggers()

    logger = Logger(component, service)
    if invalid is not None:
        logger.warn(
            "config_invalid",
            "Invalid LOG_LEVEL, using INFO",
            log_level=invalid,
        )
    return logger

#!/usr/bin/env python3
"""qBittorrent completion hook: send finished whisparr torrents to fusionn-muse.

qBittorrent setup (Options -> Downloads -> Run external program on torrent
completion):

    python3 /path/to/torrent_complete.py "%N" "%L" "%I" "%F"

%N is the torrent name, %L the category, %I the info hash and %F the content
path. Only the whisparr category is sent. The content path is mapped from the
host download mount to the path that fusionn-muse sees. The script makes one
request and does not resend. It exits 1 when the path is outside the mount, the
request fails, or fusionn-muse does not answer HTTP 202 with a submission_id;
resubmit a missed completion by hand.

Optional environment variables (the defaults are the constants below):

    MUSE_URL    fusionn-muse base URL
    LOG_FILE    file that the log lines are appended to
    LOG_LEVEL   DEBUG, INFO, WARN or ERROR; the default is INFO

The log lines use the unified format of docs/design/unified-logging.md. They go
to stderr and are appended to LOG_FILE; stdout stays empty. Every line of a run
carries the request_id that is sent to fusionn-muse in the X-Request-ID header,
and the accepted line also carries the returned submission_id. Search the
fusionn-muse container log for either value. The script needs only the Python
standard library, so it can be copied to the qBittorrent host on its own.
"""

import datetime
import http.client
import json
import os
import re
import sys
import urllib.error
import urllib.request
import uuid

MUSE_URL = os.environ.get("MUSE_URL", "http://192.168.50.135:4444")
LOG_FILE = os.environ.get("LOG_FILE", "/tmp/torrent-complete.log")

# Only this category is sent to fusionn-muse.
MUSE_CATEGORY = "whisparr"

# The host download mount, and the path that fusionn-muse sees for it.
HOST_INPUT_PATH = "/Volumes/External"
CONTAINER_INPUT_PATH = "/data"

REQUEST_TIMEOUT_SECONDS = 15
# The acknowledgement is small; reading stops at this size.
MAX_RESPONSE_BYTES = 65536

COMPONENT = "torrent-complete"

# Log timestamps are fixed UTC+8; the host TZ is not changed.
_UTC8 = datetime.timezone(datetime.timedelta(hours=8))

DEBUG, INFO, WARN, ERROR = 10, 20, 30, 40
_LEVELS = {"DEBUG": DEBUG, "INFO": INFO, "WARN": WARN, "WARNING": WARN, "ERROR": ERROR}
_LEVEL_NAMES = {DEBUG: "DEBUG", INFO: "INFO", WARN: "WARN", ERROR: "ERROR"}

# Correlation fields come right after the event, in this order. The other fields
# are sorted by name, and msg is last.
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

# Only identifier-shaped values are written without quotes.
_IDENTIFIER = re.compile(r"\A[A-Za-z0-9][A-Za-z0-9_.-]*\Z")

# The shared request ID rule. A submission_id must match it to be accepted.
_ID = re.compile(r"\A[A-Za-z0-9._-]{1,64}\Z")

# Free text that is always quoted, whatever its shape.
_ALWAYS_QUOTED = frozenset({"error"})

# Go's strconv.Quote uses these short escapes.
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


def _escape(text):
    """Escape a value like Go's strconv.Quote, without the surrounding quotes.

    Printable characters, also non-ASCII ones, stay as they are. All other
    characters get a short escape, \\xNN, \\uNNNN or \\UNNNNNNNN, so a value
    cannot split a line.
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
    """Render a field value with the shared quoting rule."""
    if value is True:
        return "true"
    if value is False:
        return "false"
    if isinstance(value, (int, float)):
        return str(value)
    text = value if isinstance(value, str) else str(value)
    if _IDENTIFIER.match(text):
        return text
    return '"%s"' % _escape(text)


def format_timestamp(moment=None):
    """Render a time as YYYY-MM-DDTHH:MM:SS.SSS+08:00."""
    moment = datetime.datetime.now(_UTC8) if moment is None else moment.astimezone(_UTC8)
    return "%s.%03d+08:00" % (moment.strftime("%Y-%m-%dT%H:%M:%S"), moment.microsecond // 1000)


def format_line(level, event, msg, fields, moment=None):
    """Render one event as one physical line."""
    fields = {key: value for key, value in fields.items() if value is not None}
    parts = [
        format_timestamp(moment),
        _LEVEL_NAMES[level],
        "component=" + COMPONENT,
        "event=" + format_value(event),
    ]
    for key in _CORRELATION:
        if key in fields:
            parts.append("%s=%s" % (key, format_value(fields.pop(key))))
    for key in sorted(fields):
        if key in _ALWAYS_QUOTED:
            parts.append('%s="%s"' % (key, _escape(str(fields[key]))))
        else:
            parts.append("%s=%s" % (key, format_value(fields[key])))
    parts.append('msg="%s"' % _escape(msg))
    return " ".join(parts)


def resolve_level():
    """Return (level, invalid value) from LOG_LEVEL; the default is INFO.

    ALIGN_LOG_LEVEL is read only when LOG_LEVEL is unset, like the other
    fusionn-muse scripts.
    """
    raw = os.environ.get("LOG_LEVEL")
    if raw is None or not raw.strip():
        raw = os.environ.get("ALIGN_LOG_LEVEL")
    if raw is None or not raw.strip():
        return INFO, None
    level = _LEVELS.get(raw.strip().upper())
    if level is None:
        return INFO, raw
    return level, None


class Log:
    """Writes the events of one run to stderr and appends them to LOG_FILE."""

    def __init__(self, level, path):
        self.level = level
        self.path = path
        # The correlation IDs of this run; every line carries them.
        self.ids = {}

    def emit(self, level, event, msg, **fields):
        if level < self.level:
            return
        fields.update(self.ids)
        line = format_line(level, event, msg, fields) + "\n"
        # qBittorrent can start the hook without a usable stderr, and LOG_FILE can
        # be unwritable. A logging failure must never stop the webhook, so each
        # copy is written on its own and its failure is ignored.
        try:
            sys.stderr.write(line)
        except (AttributeError, OSError, ValueError):
            pass
        try:
            with open(self.path, "a", encoding="utf-8") as log_file:
                log_file.write(line)
        except OSError:
            pass

    def info(self, event, msg, **fields):
        self.emit(INFO, event, msg, **fields)

    def warn(self, event, msg, **fields):
        self.emit(WARN, event, msg, **fields)

    def error(self, event, msg, **fields):
        self.emit(ERROR, event, msg, **fields)


def container_path(path):
    """Map a path under HOST_INPUT_PATH to the container path, else return None."""
    if path == HOST_INPUT_PATH or path.startswith(HOST_INPUT_PATH + "/"):
        return CONTAINER_INPUT_PATH + path[len(HOST_INPUT_PATH):]
    return None


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """A redirect is not an acknowledgement, so it is never followed."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def _submission_id(body):
    """Return the submission_id of a 202 body, or None when it is missing or not ID-shaped."""
    try:
        ack = json.loads(body)
    except ValueError:
        return None
    value = ack.get("submission_id") if isinstance(ack, dict) else None
    if isinstance(value, str) and _ID.match(value):
        return value
    return None


def submit(log, request_id, path, name, category):
    """Send the webhook request once and return the exit code.

    Only the status, the body size and fixed reasons are logged: the response
    body and the error texts can carry payload text.
    """
    payload = json.dumps({"path": path, "name": name, "category": category}, ensure_ascii=False)
    request = urllib.request.Request(
        MUSE_URL.rstrip("/") + "/api/v1/webhook/torrent",
        # surrogateescape sends undecodable argument bytes back as they came.
        data=payload.encode("utf-8", "surrogateescape"),
        headers={"Content-Type": "application/json", "X-Request-ID": request_id},
    )
    try:
        opener = urllib.request.build_opener(_NoRedirect)
        with opener.open(request, timeout=REQUEST_TIMEOUT_SECONDS) as response:
            status = response.status
            body = response.read(MAX_RESPONSE_BYTES)
    except urllib.error.HTTPError as error:
        error.close()
        log.error("webhook_failed", "Webhook request failed", error="rejected by the API", http_status=error.code)
        return 1
    except (OSError, http.client.HTTPException, ValueError) as error:
        log.error(
            "webhook_failed",
            "Webhook request failed",
            error="request transport failure",
            error_type=type(error).__name__,
        )
        return 1
    if status != 202:
        log.error(
            "webhook_failed",
            "Webhook request failed",
            error="rejected by the API",
            http_status=status,
            response_bytes=len(body),
        )
        return 1
    submission_id = _submission_id(body)
    if submission_id is None:
        log.error(
            "webhook_failed",
            "Webhook acknowledgement is invalid",
            error="missing or invalid submission_id",
            http_status=status,
            response_bytes=len(body),
        )
        return 1
    # The Submission ID links this run to the fusionn-muse Submission and Job.
    log.ids["submission_id"] = submission_id
    log.info("webhook_sent", "Webhook accepted", container_path=path, http_status=status, response_bytes=len(body))
    return 0


def main(argv):
    level, invalid_level = resolve_level()
    log = Log(level, LOG_FILE)
    # One request ID marks every line of this run and is sent in the X-Request-ID
    # header. fusionn-muse reuses it, so both logs show the same value.
    request_id = "tc-" + uuid.uuid4().hex
    log.ids["request_id"] = request_id
    if invalid_level is not None:
        log.warn("config_invalid", "Invalid LOG_LEVEL, using INFO", log_level=invalid_level)

    if len(argv) < 5:
        log.error(
            "arguments_invalid",
            "Usage: torrent_complete.py <name> <category> <info_hash> <path>",
            arguments=len(argv) - 1,
        )
        return 1
    name, category, info_hash, path = argv[1:5]
    log.info("torrent_completed", "Torrent completed", category=category, info_hash=info_hash, name=name, path=path)

    if category != MUSE_CATEGORY:
        log.info("torrent_skipped", "Category is not sent to fusionn-muse", category=category)
        return 0
    mapped = container_path(path)
    if mapped is None:
        log.error(
            "webhook_failed",
            "Path is outside the download mount",
            error="path is outside the download mount",
            host_input_path=HOST_INPUT_PATH,
            path=path,
        )
        return 1
    return submit(log, request_id, mapped, name, category)


if __name__ == "__main__":
    sys.exit(main(sys.argv))

#!/bin/bash
# ════════════════════════════════════════════════════════════════════════════
# qBittorrent Torrent Completion Script
# ════════════════════════════════════════════════════════════════════════════
#
# Setup in qBittorrent:
#   Options → Downloads → Run external program on torrent completion
#   Command: /path/to/torrent-complete.sh "%F" "%N" "%L"
#
# Parameters from qBittorrent:
#   %F = Content path (path to file or folder)
#   %N = Torrent name
#   %L = Category
#
# Environment variables:
#   MUSE_URL           - fusionn-muse API URL (default: http://localhost:8080)
#   LOG_FILE           - Log file path (default: /tmp/torrent-complete.log)
#   HOST_INPUT_PATH    - Host downloads folder (e.g., /home/user/downloads)
#   CONTAINER_INPUT_PATH - Container input folder (default: /data/input)
#
# Log lines follow the shared unified format (docs/design/unified-logging.md) and
# are written to stderr and, when LOG_FILE is set, appended to that file. This
# host script is not part of the container, so its log does not appear in
# `docker compose logs`.
# ════════════════════════════════════════════════════════════════════════════

# Configuration
MUSE_URL="${MUSE_URL:-http://localhost:8080}"
LOG_FILE="${LOG_FILE:-/tmp/torrent-complete.log}"

# Log timestamps are fixed UTC+8 and never change the host TZ. "Etc/GMT-8" is
# POSIX-inverted: it is UTC+8.
LOG_COMPONENT="torrent-complete"
LOG_TZ="Etc/GMT-8"

# Path mapping (host downloads → container input)
# Required when fusionn-muse runs in Docker
HOST_INPUT_PATH="${HOST_INPUT_PATH:-}"
CONTAINER_INPUT_PATH="${CONTAINER_INPUT_PATH:-/data/input}"

# Parameters
CONTENT_PATH="$1"
TORRENT_NAME="$2"
CATEGORY="$3"

# Print "<codepoint> <byte length>" for the UTF-8 sequence at byte offset i.
# Bash 3.2 cannot decode UTF-8 itself, so the bytes are combined by hand.
utf8_codepoint() {
    local value="$1" i="$2" b2 b3 b4 lead
    local LC_ALL=C
    printf -v lead '%d' "'${value:i:1}"
    ((lead < 0)) && ((lead += 256))
    if ((lead < 0xC0)); then
        printf '%d 1' "$lead"
        return
    fi
    printf -v b2 '%d' "'${value:i+1:1}"
    ((b2 < 0)) && ((b2 += 256))
    if ((lead < 0xE0)); then
        printf '%d 2' "$((((lead & 0x1F) << 6) | (b2 & 0x3F)))"
        return
    fi
    printf -v b3 '%d' "'${value:i+2:1}"
    ((b3 < 0)) && ((b3 += 256))
    if ((lead < 0xF0)); then
        printf '%d 3' "$((((lead & 0x0F) << 12) | ((b2 & 0x3F) << 6) | (b3 & 0x3F)))"
        return
    fi
    printf -v b4 '%d' "'${value:i+3:1}"
    ((b4 < 0)) && ((b4 += 256))
    printf '%d 4' "$((((lead & 0x07) << 18) | ((b2 & 0x3F) << 12) | ((b3 & 0x3F) << 6) | (b4 & 0x3F)))"
}

# The non-printable codepoints Go's strconv.Quote escapes. Normal text, including
# CJK and emoji, is kept as is.
utf8_needs_escape() {
    local code="$1"
    ((code < 0x20)) && return 0
    ((code == 0x7f)) && return 0
    ((code >= 0x80 && code <= 0x9f)) && return 0 # C1 controls
    ((code == 0xa0 || code == 0xad)) && return 0 # no-break space, soft hyphen
    ((code >= 0x2000 && code <= 0x200f)) && return 0
    ((code >= 0x2028 && code <= 0x202f)) && return 0 # line/paragraph separator, bidi
    ((code >= 0x205f && code <= 0x2064)) && return 0
    ((code == 0x3000 || code == 0xfeff)) && return 0
    return 1
}

# Escape one value. Bash 3.2 cannot decode UTF-8 itself, so the bytes are
# combined by hand and the shared non-printable characters are escaped. The
# caller decides whether the result needs quotes.
log_escape() {
    local value="$1" escaped='' char esc i code length
    local LC_ALL=C
    for ((i=0; i<${#value}; i++)); do
        char="${value:i:1}"
        case "$char" in
            '\') escaped+='\\' ;;
            '"') escaped+='\"' ;;
            $'\a') escaped+='\a' ;;
            $'\b') escaped+='\b' ;;
            $'\f') escaped+='\f' ;;
            $'\n') escaped+='\n' ;;
            $'\r') escaped+='\r' ;;
            $'\t') escaped+='\t' ;;
            $'\v') escaped+='\v' ;;
            *)
                # printf reports a byte above 0x7f as a negative number.
                printf -v code '%d' "'$char"
                ((code < 0)) && ((code += 256))
                if ((code >= 32 && code < 127)); then
                    escaped+="$char"
                    continue
                fi
                if ((code < 128)); then
                    printf -v esc '\\x%02x' "$code"
                    escaped+="$esc"
                    continue
                fi
                read -r code length <<< "$(utf8_codepoint "$value" "$i")"
                if utf8_needs_escape "$code"; then
                    printf -v esc '\\u%04x' "$code"
                    escaped+="$esc"
                else
                    escaped+="${value:i:length}"
                fi
                ((i += length - 1))
                ;;
        esac
    done
    printf '%s' "$escaped"
}

# Quote one value using the same rules as the Go and Python services (Go's
# strconv.Quote): identifier-shaped values stay bare, everything else is double
# quoted.
log_value() {
    local value="$1"
    if [[ -n "$value" && "$value" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]]; then
        printf '%s' "$value"
        return
    fi
    printf '"%s"' "$(log_escape "$value")"
}

log_timestamp() {
    local stamp base rest fraction offset
    stamp="$(TZ="$LOG_TZ" date '+%Y-%m-%dT%H:%M:%S|%N|%z')"
    base="${stamp%%|*}"
    rest="${stamp#*|}"
    fraction="${rest%%|*}"
    offset="${rest##*|}"
    if [[ "$TIME_PRECISION" == millisecond ]]; then
        fraction="${fraction:0:3}"
    else
        # Host date cannot report sub-second time; see detect_time_precision.
        fraction='000'
    fi
    printf '%s.%s%s:%s' "$base" "$fraction" "${offset:0:3}" "${offset:3:2}"
}

# Millisecond timestamps are used only when `date` really reports them. Otherwise
# every line is marked time_precision=second instead of implying precision.
detect_time_precision() {
    if [[ "$(TZ="$LOG_TZ" date '+%N')" =~ ^[0-9]{3} ]]; then
        TIME_PRECISION=millisecond
    else
        TIME_PRECISION=second
    fi
}

# LOG_LEVEL selects what is displayed and defaults to INFO. ALIGN_LOG_LEVEL is
# read only when LOG_LEVEL is unset, matching the container services.
resolve_log_level() {
    local raw="${LOG_LEVEL:-${ALIGN_LOG_LEVEL:-INFO}}"
    INVALID_LEVEL=''
    case "$raw" in
        [Dd][Ee][Bb][Uu][Gg]) DISPLAY_LEVEL=DEBUG ;;
        ""|[Ii][Nn][Ff][Oo]) DISPLAY_LEVEL=INFO ;;
        [Ww][Aa][Rr][Nn]|[Ww][Aa][Rr][Nn][Ii][Nn][Gg]) DISPLAY_LEVEL=WARN ;;
        [Ee][Rr][Rr][Oo][Rr]) DISPLAY_LEVEL=ERROR ;;
        *) DISPLAY_LEVEL=INFO; INVALID_LEVEL="$raw" ;;
    esac
}

level_rank() {
    case "$1" in
        DEBUG) printf '0' ;;
        WARN) printf '2' ;;
        ERROR) printf '3' ;;
        *) printf '1' ;;
    esac
}

# log_line <LEVEL> <event> <msg> [key=value ...]
# Fields are sorted by name, matching the Go and Python renderers. Values are
# escaped before sorting so an embedded newline cannot split or reorder a line.
# shortcut: correlation fields (job_id ... stream) are not emitted here; add the
# fixed-order prefix if this script ever learns a Job or Submission id.
log_line() {
    local level="$1" event="$2" msg="$3"
    shift 3
    if (($(level_rank "$level") < $(level_rank "$DISPLAY_LEVEL"))); then
        return
    fi
    local rendered=() pair key
    for pair in "$@"; do
        key="${pair%%=*}"
        [[ "$key" =~ ^[a-z][a-z0-9_]*$ ]] || continue
        case "$key" in
            service|component|event|msg|level|time|timestamp) continue ;;
        esac
        rendered+=("$key=$(log_value "${pair#*=}")")
    done
    if [[ "$TIME_PRECISION" == second ]]; then
        rendered+=("time_precision=second")
    fi
    local line
    line="$(log_timestamp) $level component=$LOG_COMPONENT event=$event"
    if ((${#rendered[@]} > 0)); then
        while IFS= read -r pair; do
            line+=" $pair"
        done < <(printf '%s\n' "${rendered[@]}" | LC_ALL=C sort)
    fi
    line+=" msg=\"$(log_escape "$msg")\""
    printf '%s\n' "$line" | tee -a "$LOG_FILE" >&2
}

# Translate host path to container path if mapping is configured
ORIGINAL_PATH="$CONTENT_PATH"
if [[ -n "$HOST_INPUT_PATH" ]]; then
    CONTENT_PATH="${CONTENT_PATH/#$HOST_INPUT_PATH/$CONTAINER_INPUT_PATH}"
fi

# Main
detect_time_precision
resolve_log_level
if [[ -n "$INVALID_LEVEL" ]]; then
    log_line WARN config_invalid "Invalid LOG_LEVEL, using INFO" "log_level=$INVALID_LEVEL"
fi
if [[ "$TIME_PRECISION" == second ]]; then
    log_line WARN time_precision_degraded "Sub-second timestamps unavailable; using second precision"
fi

if [[ "$ORIGINAL_PATH" != "$CONTENT_PATH" ]]; then
    log_line INFO torrent_completed "Torrent completed" "name=$TORRENT_NAME" "path=$CONTENT_PATH" "source_path=$ORIGINAL_PATH" "category=$CATEGORY"
else
    log_line INFO torrent_completed "Torrent completed" "name=$TORRENT_NAME" "path=$CONTENT_PATH" "category=$CATEGORY"
fi

# Encode JSON strings without requiring Python or jq in the qBittorrent container.
json_string() {
    local value="$1" encoded='"' char code i
    local LC_ALL=C
    for ((i=0; i<${#value}; i++)); do
        char="${value:i:1}"
        case "$char" in
            '"') encoded+='\"' ;;
            '\') encoded+='\\' ;;
            *)
                printf -v code '%d' "'$char"
                if ((code >= 0 && code < 32)); then
                    printf -v char '\\u%04x' "$code"
                fi
                encoded+="$char"
                ;;
        esac
    done
    printf '%s"' "$encoded"
}

PAYLOAD="{\"path\":$(json_string "$CONTENT_PATH"),\"name\":$(json_string "$TORRENT_NAME"),\"category\":$(json_string "$CATEGORY")}"

# One bounded attempt. Unsent notifications are resubmitted manually, not queued.
RESPONSE=$(curl -sS --connect-timeout 5 --max-time 15 \
    -X POST "${MUSE_URL%/}/api/v1/webhook/torrent" \
    -H "Content-Type: application/json" \
    --data "$PAYLOAD" --write-out $'\n%{http_code}' 2>&1)
CURL_STATUS=$?
HTTP_STATUS="${RESPONSE##*$'\n'}"
RESPONSE="${RESPONSE%$'\n'*}"

if [[ "$CURL_STATUS" -eq 0 && "$HTTP_STATUS" == "202" ]]; then
    log_line INFO webhook_sent "Webhook accepted" "http_status=$HTTP_STATUS" "response_bytes=${#RESPONSE}"
    # A logging failure must not turn an accepted webhook into a reported failure.
    exit 0
fi

# Only the status code, the transport result and a fixed reason are recorded: the
# response body and curl's message can carry credentials or payload text.
RESULT_FIELDS=("curl_status=$CURL_STATUS" "response_bytes=${#RESPONSE}")
if [[ "$HTTP_STATUS" =~ ^[0-9]{3}$ ]]; then
    RESULT_FIELDS+=("http_status=$HTTP_STATUS")
fi
if [[ "$CURL_STATUS" -eq 0 ]]; then
    RESULT_FIELDS+=("error=rejected by the API")
else
    RESULT_FIELDS+=("error=request transport failure")
fi
log_line ERROR webhook_failed "Webhook request failed" "${RESULT_FIELDS[@]}"
exit 1

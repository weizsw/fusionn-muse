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
# ════════════════════════════════════════════════════════════════════════════

# Configuration
MUSE_URL="${MUSE_URL:-http://localhost:8080}"
LOG_FILE="${LOG_FILE:-/tmp/torrent-complete.log}"

# Path mapping (host downloads → container input)
# Required when fusionn-muse runs in Docker
HOST_INPUT_PATH="${HOST_INPUT_PATH:-}"
CONTAINER_INPUT_PATH="${CONTAINER_INPUT_PATH:-/data/input}"

# Parameters
CONTENT_PATH="$1"
TORRENT_NAME="$2"
CATEGORY="$3"

# Log function
log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" >> "$LOG_FILE"
}

# Translate host path to container path if mapping is configured
ORIGINAL_PATH="$CONTENT_PATH"
if [[ -n "$HOST_INPUT_PATH" ]]; then
    CONTENT_PATH="${CONTENT_PATH/#$HOST_INPUT_PATH/$CONTAINER_INPUT_PATH}"
fi

# Main
log "Torrent complete: $TORRENT_NAME"
if [[ "$ORIGINAL_PATH" != "$CONTENT_PATH" ]]; then
    log "  Path: $ORIGINAL_PATH → $CONTENT_PATH"
else
    log "  Path: $CONTENT_PATH"
fi
log "  Category: $CATEGORY"

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
    log "  Webhook sent successfully (HTTP 202): $RESPONSE"
else
    log "  Webhook failed (curl=$CURL_STATUS, HTTP $HTTP_STATUS): $RESPONSE"
    exit 1
fi


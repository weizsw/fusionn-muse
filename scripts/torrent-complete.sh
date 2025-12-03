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

# Send webhook to fusionn-muse
RESPONSE=$(curl -s -X POST "$MUSE_URL/api/v1/webhook/torrent" \
    -H "Content-Type: application/json" \
    -d "{\"path\": \"$CONTENT_PATH\", \"name\": \"$TORRENT_NAME\", \"category\": \"$CATEGORY\"}" \
    2>&1)

if [ $? -eq 0 ]; then
    log "  Webhook sent successfully: $RESPONSE"
else
    log "  Webhook failed: $RESPONSE"
fi


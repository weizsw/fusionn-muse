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
# ════════════════════════════════════════════════════════════════════════════

# Configuration
MUSE_URL="${MUSE_URL:-http://localhost:8080}"
LOG_FILE="${LOG_FILE:-/tmp/torrent-complete.log}"

# Parameters
CONTENT_PATH="$1"
TORRENT_NAME="$2"
CATEGORY="$3"

# Log function
log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" >> "$LOG_FILE"
}

# Main
log "Torrent complete: $TORRENT_NAME"
log "  Path: $CONTENT_PATH"
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


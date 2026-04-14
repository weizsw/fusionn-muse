#!/bin/bash

input=$(cat)
file_path=$(echo "$input" | jq -r '.file_path // empty')

if [[ "$file_path" != *.go ]]; then
  exit 0
fi

cd "$CURSOR_PROJECT_DIR" || exit 0

pkg_dir=$(dirname "$file_path")
output=$(golangci-lint run "./$pkg_dir/..." 2>&1)
exit_code=$?

if [ $exit_code -ne 0 ]; then
  jq -n --arg ctx "golangci-lint found issues after editing $file_path:\n$output\n\nFix these before proceeding." \
    '{"additional_context": $ctx}'
else
  exit 0
fi

#!/usr/bin/env bash
set -euo pipefail

target="${1:?target triple is required}"
directory="desktop/src-tauri/binaries"

if [[ -z "${FFMPEG_URL:-}" || -z "${FFMPEG_SHA256:-}" || -z "${FFPROBE_URL:-}" || -z "${FFPROBE_SHA256:-}" ]]; then
  echo "The audited FFmpeg and ffprobe URLs and SHA-256 values are required." >&2
  exit 1
fi

ffmpeg="$directory/ffmpeg-$target"
ffprobe="$directory/ffprobe-$target"

curl --fail --location --proto '=https' --tlsv1.2 "$FFMPEG_URL" --output "$ffmpeg"
curl --fail --location --proto '=https' --tlsv1.2 "$FFPROBE_URL" --output "$ffprobe"

echo "$FFMPEG_SHA256  $ffmpeg" | shasum --algorithm 256 --check
echo "$FFPROBE_SHA256  $ffprobe" | shasum --algorithm 256 --check
chmod 0755 "$ffmpeg" "$ffprobe"

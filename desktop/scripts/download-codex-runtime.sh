#!/usr/bin/env bash
set -euo pipefail

: "${CODEX_RUNTIME_URL:?set CODEX_RUNTIME_URL to the reviewed Codex CLI release asset}"
: "${CODEX_RUNTIME_SHA256:?set CODEX_RUNTIME_SHA256 to the published or independently verified SHA-256}"
: "${TAURI_ENV_TARGET_TRIPLE:?set TAURI_ENV_TARGET_TRIPLE to the Rust target triple}"

directory="$(cd "$(dirname "$0")/../src-tauri/binaries" && pwd)"
suffix=""
if [[ "$TAURI_ENV_TARGET_TRIPLE" == *windows* ]]; then suffix=".exe"; fi
target="$directory/shotforward-codex-runtime-$TAURI_ENV_TARGET_TRIPLE$suffix"
download="$(mktemp "$directory/.codex-runtime-download.XXXXXX")"
extracted="$(mktemp -d "$directory/.codex-runtime-extract.XXXXXX")"
cleanup() { rm -f -- "$download"; rm -rf -- "$extracted"; }
trap cleanup EXIT

curl --fail --location --proto '=https' --tlsv1.2 "$CODEX_RUNTIME_URL" --output "$download"
echo "$CODEX_RUNTIME_SHA256  $download" | shasum -a 256 --check

case "$CODEX_RUNTIME_URL" in
  *.tar.gz)
    tar --extract --gzip --file "$download" --directory "$extracted" --no-same-owner
    source_binary="$extracted/codex-$TAURI_ENV_TARGET_TRIPLE$suffix"
    [[ -f "$source_binary" ]] || { echo "Codex archive does not contain $source_binary" >&2; exit 1; }
    mv -- "$source_binary" "$target"
    ;;
  *)
    mv -- "$download" "$target"
    ;;
esac
chmod 0755 "$target"
"$target" --version

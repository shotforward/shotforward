#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: sign-macos-release.sh <unsigned-payload.tar.gz> <developer-id.p12> [output-directory]

Required environment variables:
  APPLE_ID                    Apple account email
  APPLE_PASSWORD              Apple app-specific password
  APPLE_TEAM_ID               Apple Developer Team ID

Optional environment variable:
  APPLE_CERTIFICATE_PASSWORD  .p12 password; prompted securely when omitted
EOF
}

[[ $# -ge 2 && $# -le 3 ]] || { usage >&2; exit 2; }
[[ "$(uname -s)" == "Darwin" ]] || { echo "This script must run on macOS." >&2; exit 1; }

payload="$1"
certificate="$2"
output_argument="${3:-release-output/macos}"
[[ -f "$payload" ]] || { echo "Payload not found: $payload" >&2; exit 1; }
[[ -f "$certificate" ]] || { echo "Certificate not found: $certificate" >&2; exit 1; }
: "${APPLE_ID:?set APPLE_ID to the Apple account email}"
: "${APPLE_PASSWORD:?set APPLE_PASSWORD to an app-specific password}"
: "${APPLE_TEAM_ID:?set APPLE_TEAM_ID to the Apple Developer Team ID}"

repository="$(cd "$(dirname "$0")/../.." && pwd)"
if [[ "$output_argument" = /* ]]; then
  output_directory="$output_argument"
else
  output_directory="$repository/$output_argument"
fi
temporary="$(mktemp -d "${TMPDIR:-/tmp}/shotforward-macos-signing.XXXXXX")"
extract="$temporary/payload"
keychain="$temporary/shotforward-signing.keychain-db"
keychain_password="$(openssl rand -hex 32)"
original_default_keychain="$(security default-keychain -d user | tr -d '"')"
original_keychains=()
while IFS= read -r existing_keychain; do
  existing_keychain="${existing_keychain#\"}"
  existing_keychain="${existing_keychain%\"}"
  [[ -n "$existing_keychain" ]] && original_keychains+=("$existing_keychain")
done < <(security list-keychains -d user | sed -E 's/^[[:space:]]+//')
bundle_config="$temporary/tauri.macos-signing.conf.json"

cleanup() {
  if [[ -n "$original_default_keychain" ]]; then
    security default-keychain -d user -s "$original_default_keychain" >/dev/null 2>&1 || true
  fi
  if (( ${#original_keychains[@]} > 0 )); then
    security list-keychains -d user -s "${original_keychains[@]}" >/dev/null 2>&1 || true
  fi
  security delete-keychain "$keychain" >/dev/null 2>&1 || true
  rm -rf "$temporary"
}
trap cleanup EXIT

mkdir -p "$extract"
tar -xzf "$payload" -C "$extract"
(
  cd "$extract"
  shasum -a 256 --check SHA256SUMS.txt
)

manifest="$extract/payload-manifest.json"
[[ -f "$manifest" ]] || { echo "The payload does not contain payload-manifest.json" >&2; exit 1; }
read_manifest() {
  node -e "const value=require(process.argv[1]);const result=value[process.argv[2]];if(result===undefined)process.exit(2);process.stdout.write(String(result))" "$manifest" "$1"
}
schema_version="$(read_manifest schema_version)"
platform="$(read_manifest platform)"
target="$(read_manifest target)"
version="$(read_manifest version)"
commit="$(read_manifest commit)"
[[ "$schema_version" == "1" && "$platform" == macos-* && "$target" == *-apple-darwin ]] || {
  echo "The payload is not a supported ShotForward macOS payload" >&2
  exit 1
}

current_commit="$(git -C "$repository" rev-parse HEAD)"
[[ "$current_commit" == "$commit" ]] || {
  echo "Payload commit $commit does not match the checked-out commit $current_commit" >&2
  exit 1
}

release_directory="$repository/desktop/src-tauri/target/$target/release"
binary_directory="$repository/desktop/src-tauri/binaries"
mkdir -p "$release_directory" "$binary_directory"
cp "$extract/desktop/src-tauri/target/$target/release/shotforward-desktop" "$release_directory/"
cp "$extract"/desktop/src-tauri/binaries/*-"$target" "$binary_directory/"
chmod 0755 "$release_directory/shotforward-desktop" "$binary_directory"/*-"$target"

VERSION="$version" node -e "const fs=require('fs');fs.writeFileSync(process.argv[1],JSON.stringify({version:process.env.VERSION},null,2)+'\n')" "$bundle_config"

if [[ ! -x "$repository/desktop/node_modules/.bin/tauri" ]]; then
  npm ci --prefix "$repository/desktop"
fi

certificate_password="${APPLE_CERTIFICATE_PASSWORD:-}"
if [[ -z "$certificate_password" ]]; then
  read -r -s -p "P12 password: " certificate_password
  echo
fi

security create-keychain -p "$keychain_password" "$keychain"
security set-keychain-settings -lut 21600 "$keychain"
security unlock-keychain -p "$keychain_password" "$keychain"
security import "$certificate" -k "$keychain" -P "$certificate_password" -T /usr/bin/codesign -T /usr/bin/security
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$keychain_password" "$keychain"
security default-keychain -d user -s "$keychain"
security list-keychains -d user -s "$keychain" "${original_keychains[@]}"

identity="$(security find-identity -v -p codesigning "$keychain" | awk -F'"' '/Developer ID Application/ { print $2; exit }')"
[[ -n "$identity" ]] || { echo "No Developer ID Application identity was found in the P12." >&2; exit 1; }

export APPLE_SIGNING_IDENTITY="$identity"
export MACOSX_DEPLOYMENT_TARGET="12.0"

(
  cd "$repository/desktop"
  npm run tauri -- bundle --ci --target "$target" --bundles app,dmg --config "$bundle_config"
)

app="$(find "$release_directory/bundle/macos" -maxdepth 1 -type d -name '*.app' -print -quit)"
dmg="$(find "$release_directory/bundle/dmg" -maxdepth 1 -type f -name '*.dmg' -print -quit)"
[[ -n "$app" && -n "$dmg" ]] || { echo "Tauri did not produce both an app bundle and a DMG." >&2; exit 1; }

codesign --verify --deep --strict --verbose=2 "$app"
spctl --assess --type execute --verbose=2 "$app"
spctl --assess --type open --context context:primary-signature --verbose=2 "$dmg"
xcrun stapler validate "$app"
xcrun stapler validate "$dmg"

architecture="${platform#macos-}"
destination="$output_directory/$architecture"
mkdir -p "$destination"
cp "$dmg" "$destination/"
(
  cd "$destination"
  shasum -a 256 "$(basename "$dmg")" > "SHA256SUMS-macos-$architecture.txt"
)

echo "Signed and notarized macOS release written to $destination"

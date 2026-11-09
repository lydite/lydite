#!/usr/bin/env bash
# Hashes what the committed dashboard bundle is built from, so a change to the
# dashboard that was never rebuilt fails CI instead of shipping the previous
# bundle silently (ADR 0010, "The dashboard bundle is committed, and guarded").
#
# The inputs are src/**, index.html, pnpm-lock.yaml and vite.config.ts. Each
# file is hashed on its own, the "<digest>  <path>" lines are sorted bytewise
# with paths relative to source/web, and that listing is hashed again — so the
# result depends on file contents and names only, never on where the checkout
# lives, on a locale's collation, or on whether the host ships sha256sum
# (Linux) or shasum (macOS). The check needs no Node and does not depend on
# Vite producing byte-identical output across machines.
#
# Usage:
#   scripts/bundle-hash.sh           print the hash of the current inputs
#   scripts/bundle-hash.sh --write   record it in bundle.sha256
#   scripts/bundle-hash.sh --check   fail unless bundle.sha256 matches it
set -euo pipefail
export LC_ALL=C

web="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$web"

recorded=bundle.sha256
inputs=(index.html pnpm-lock.yaml vite.config.ts)

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 | cut -d' ' -f1; }
else
  echo "bundle-hash: neither sha256sum nor shasum is on PATH" >&2
  exit 2
fi

compute() {
  local f
  for f in "${inputs[@]}"; do
    if [ ! -f "$f" ]; then
      echo "bundle-hash: source/web/$f is missing, and it is one of the bundle's inputs" >&2
      return 2
    fi
  done
  if [ ! -d src ]; then
    echo "bundle-hash: source/web/src is missing, and it is one of the bundle's inputs" >&2
    return 2
  fi
  {
    for f in "${inputs[@]}"; do printf '%s\0' "$f"; done
    find src -type f -print0
  } | sort -z | while IFS= read -r -d '' f; do
    printf '%s  %s\n' "$(sha256 <"$f")" "$f"
  done | sha256
}

case "${1:-}" in
  "")
    compute
    ;;
  --write)
    hash="$(compute)"
    printf '%s\n' "$hash" >"$recorded"
    echo "bundle-hash: recorded $hash in source/web/$recorded"
    ;;
  --check)
    hash="$(compute)"
    if [ ! -f "$recorded" ]; then
      echo "bundle-hash: source/web/$recorded is missing; build the bundle and record its hash:" >&2
      echo "  cd source/web && pnpm install --frozen-lockfile && pnpm build && scripts/bundle-hash.sh --write" >&2
      exit 1
    fi
    if [ ! -f dist/index.html ]; then
      echo "bundle-hash: source/web/dist/index.html is missing, so there is no committed bundle to embed; rebuild it:" >&2
      echo "  cd source/web && pnpm install --frozen-lockfile && pnpm build && scripts/bundle-hash.sh --write" >&2
      exit 1
    fi
    want="$(tr -d '[:space:]' <"$recorded")"
    if [ "$hash" != "$want" ]; then
      echo "bundle-hash: the dashboard's inputs changed and the committed bundle was not rebuilt." >&2
      echo "  recorded in source/web/$recorded: $want" >&2
      echo "  computed from the current inputs:  $hash" >&2
      echo "Rebuild the bundle, record the new hash, and commit both dist/ and $recorded:" >&2
      echo "  cd source/web && pnpm install --frozen-lockfile && pnpm build && scripts/bundle-hash.sh --write" >&2
      exit 1
    fi
    echo "bundle-hash: source/web/$recorded matches the current inputs ($hash)"
    ;;
  *)
    echo "usage: scripts/bundle-hash.sh [--write | --check]" >&2
    exit 2
    ;;
esac

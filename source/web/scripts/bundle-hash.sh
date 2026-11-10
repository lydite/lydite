#!/usr/bin/env bash
# Ties the committed dashboard bundle to what it is built from, so a change to
# the dashboard that was never rebuilt fails CI instead of shipping the previous
# bundle silently, and a bundle edited by hand fails it too (ADR 0010, "The
# dashboard bundle is committed, and guarded").
#
# bundle.sha256 records two digests, one per line, each "<name> <hex>":
#
#   inputs <hex>   over src/**, index.html, pnpm-lock.yaml and vite.config.ts
#   dist <hex>     over every file under dist/, the bytes that ship
#
# Each digest is computed the same way: every file is hashed on its own, the
# "<digest>  <path>" lines are sorted bytewise with paths relative to
# source/web, and that listing is hashed again — so a digest depends on file
# contents and names only, never on where the checkout lives, on a locale's
# collation, or on whether the host ships sha256sum (Linux) or shasum (macOS).
#
# --check fails when either recomputed digest differs from its recorded one: an
# input changed without a rebuild and re-record, or dist/ changed without a
# re-record. Neither needs Node, and neither depends on Vite producing
# byte-identical output across machines — nothing here rebuilds the bundle.
#
# --check alone cannot catch a dist/ edited by hand and re-recorded, because the
# re-record makes the file agree with itself. --check-against <ref> closes that:
# it reads bundle.sha256 as recorded at <ref> and fails when the recorded dist
# digest moved while the recorded inputs digest did not. A legitimate rebuild
# always follows a change to something the bundle is built from (the lockfile
# included), so a bundle that changed on its own is the one this rejects. When
# <ref> records no dist digest — no bundle.sha256 there yet, or one recording
# the inputs digest alone — there is nothing to compare against, and the
# comparison is skipped with a message saying so.
#
# Usage:
#   scripts/bundle-hash.sh                       print both current digests
#   scripts/bundle-hash.sh --write               record them in bundle.sha256
#   scripts/bundle-hash.sh --check               fail unless bundle.sha256 matches them
#   scripts/bundle-hash.sh --check-against REF   --check, then fail if dist moved
#                                                without inputs moving since REF
set -euo pipefail
export LC_ALL=C

web="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$web"

recorded=bundle.sha256
inputs=(index.html pnpm-lock.yaml vite.config.ts)
rebuild="  cd source/web && pnpm install --frozen-lockfile && pnpm build && scripts/bundle-hash.sh --write"

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 | cut -d' ' -f1; }
else
  echo "bundle-hash: neither sha256sum nor shasum is on PATH" >&2
  exit 2
fi

# digest reads NUL-separated paths on stdin and prints the hash of their sorted
# "<digest>  <path>" listing.
digest() {
  local f
  sort -z | while IFS= read -r -d '' f; do
    printf '%s  %s\n' "$(sha256 <"$f")" "$f"
  done | sha256
}

compute_inputs() {
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
  } | digest
}

compute_dist() {
  if [ ! -f dist/index.html ]; then
    echo "bundle-hash: source/web/dist/index.html is missing, so there is no committed bundle to embed; rebuild it:" >&2
    echo "$rebuild" >&2
    return 1
  fi
  find dist -type f -print0 | digest
}

# field prints the hex recorded under name in the bundle.sha256 text on stdin,
# or nothing when that text records no such line.
field() {
  awk -v name="$1" '$1 == name && NF == 2 { print $2; exit }'
}

check() {
  local inputs_now dist_now inputs_want dist_want failed=0
  if [ ! -f "$recorded" ]; then
    echo "bundle-hash: source/web/$recorded is missing; build the bundle and record its digests:" >&2
    echo "$rebuild" >&2
    return 1
  fi
  inputs_now="$(compute_inputs)"
  dist_now="$(compute_dist)"
  inputs_want="$(field inputs <"$recorded")"
  dist_want="$(field dist <"$recorded")"
  if [ -z "$inputs_want" ] || [ -z "$dist_want" ]; then
    echo "bundle-hash: source/web/$recorded does not record both an \"inputs\" and a \"dist\" digest; rebuild and re-record:" >&2
    echo "$rebuild" >&2
    return 1
  fi
  if [ "$inputs_now" != "$inputs_want" ]; then
    echo "bundle-hash: the dashboard's inputs changed and the committed bundle was not rebuilt." >&2
    echo "  inputs recorded in source/web/$recorded: $inputs_want" >&2
    echo "  inputs computed from the current tree:   $inputs_now" >&2
    failed=1
  fi
  if [ "$dist_now" != "$dist_want" ]; then
    echo "bundle-hash: the committed bundle under source/web/dist does not match the dist digest recorded for it." >&2
    echo "  dist recorded in source/web/$recorded: $dist_want" >&2
    echo "  dist computed from the current tree:   $dist_now" >&2
    echo "  dist/ is build output: change it only by rebuilding, never by hand." >&2
    failed=1
  fi
  if [ "$failed" -ne 0 ]; then
    echo "Rebuild the bundle, record its digests, and commit both dist/ and $recorded:" >&2
    echo "$rebuild" >&2
    return 1
  fi
  echo "bundle-hash: source/web/$recorded matches the current inputs ($inputs_now) and dist ($dist_now)"
}

check_against() {
  local ref="$1" path base base_inputs base_dist head_inputs head_dist
  if ! git rev-parse --verify --quiet "$ref^{commit}" >/dev/null; then
    echo "bundle-hash: $ref does not name a commit in this repository" >&2
    return 2
  fi
  check
  path="$(git rev-parse --show-prefix)$recorded"
  if ! git cat-file -e "$ref:$path" 2>/dev/null; then
    echo "bundle-hash: $ref has no $path, so the bundle is new there; nothing to compare against"
    return 0
  fi
  base="$(git show "$ref:$path")"
  base_inputs="$(field inputs <<<"$base")"
  base_dist="$(field dist <<<"$base")"
  if [ -z "$base_inputs" ] || [ -z "$base_dist" ]; then
    echo "bundle-hash: $path at $ref records no dist digest; nothing to compare the bundle against"
    return 0
  fi
  head_inputs="$(field inputs <"$recorded")"
  head_dist="$(field dist <"$recorded")"
  if [ "$head_dist" != "$base_dist" ] && [ "$head_inputs" = "$base_inputs" ]; then
    echo "bundle-hash: the committed bundle changed since $ref, and nothing it is built from did." >&2
    echo "  inputs, recorded identically at $ref and now: $head_inputs" >&2
    echo "  dist recorded at $ref: $base_dist" >&2
    echo "  dist recorded now:     $head_dist" >&2
    echo "A rebuild follows a change to src/**, index.html, pnpm-lock.yaml or vite.config.ts;" >&2
    echo "a bundle that changed on its own was edited by hand. Restore dist/ and $recorded from $ref." >&2
    return 1
  fi
  echo "bundle-hash: the bundle has not changed without its inputs changing since $ref"
}

case "${1:-}" in
  "")
    printf 'inputs %s\ndist %s\n' "$(compute_inputs)" "$(compute_dist)"
    ;;
  --write)
    inputs_now="$(compute_inputs)"
    dist_now="$(compute_dist)"
    printf 'inputs %s\ndist %s\n' "$inputs_now" "$dist_now" >"$recorded"
    echo "bundle-hash: recorded inputs $inputs_now and dist $dist_now in source/web/$recorded"
    ;;
  --check)
    check
    ;;
  --check-against)
    if [ -z "${2:-}" ]; then
      echo "usage: scripts/bundle-hash.sh --check-against <git-ref>" >&2
      exit 2
    fi
    check_against "$2"
    ;;
  *)
    echo "usage: scripts/bundle-hash.sh [--write | --check | --check-against <git-ref>]" >&2
    exit 2
    ;;
esac

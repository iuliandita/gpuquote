#!/usr/bin/env bash
set -euo pipefail

tracked_local='(^|/)(AGENTS\.md|CLAUDE\.md|ROADMAP\.md|\.claude/|\.codex/|\.cursor/|\.handoff/|\.env($|\.)|coverage\.out$|[^/]*\.prof$)|^(docs/local/|data/local/|bin/|dist/)'
violations="$(git ls-files | grep -E "${tracked_local}" | grep -Ev '(^|/)\.env\.example$' || true)"
if [[ -n "${violations}" ]]; then
  printf 'tracked local-only artifact detected\n' >&2
  printf '%s\n' "${violations}" >&2
  exit 1
fi

private_context='(/home/[^/[:space:]]+/|file://|10\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|192\.168\.[0-9]{1,3}\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[01])\.[0-9]{1,3}\.[0-9]{1,3})'
if git grep --untracked --exclude-standard -nEI "${private_context}" -- ':!scripts/check-public.sh'; then
  printf 'machine-specific path or private-network address detected\n' >&2
  exit 1
fi

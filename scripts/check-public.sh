#!/usr/bin/env bash
set -euo pipefail

grep_allow_no_match() {
  local status=0
  grep "$@" || status=$?
  if [[ "${status}" -eq 1 ]]; then
    return 0
  fi
  return "${status}"
}

tracked_local='(^|/)(AGENTS\.md|CLAUDE\.md|ROADMAP\.md|\.claude/|\.codex/|\.cursor/|\.handoff/|\.env($|\.)|coverage\.out$|[^/]*\.prof$)|^(docs/local/|data/local/|bin/|dist/)'
tracked_files="$(git ls-files)"
violations="$(
  printf '%s\n' "${tracked_files}" |
    grep_allow_no_match -E "${tracked_local}" |
    grep_allow_no_match -Ev '(^|/)\.env\.example$'
)"
if [[ -n "${violations}" ]]; then
  printf 'tracked local-only artifact detected\n' >&2
  printf '%s\n' "${violations}" >&2
  exit 1
fi

private_context='(/home/[^/[:space:]]+/|file://|10\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|192\.168\.[0-9]{1,3}\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[01])\.[0-9]{1,3}\.[0-9]{1,3})'
private_matches=""
git_grep_status=0
private_matches="$(git grep --untracked --exclude-standard -nEI "${private_context}" -- ':!scripts/check-public.sh')" || git_grep_status=$?
case "${git_grep_status}" in
  0)
    printf '%s\n' "${private_matches}"
    printf 'machine-specific path or private-network address detected\n' >&2
    exit 1
    ;;
  1) ;;
  *)
    printf 'public content scan failed: git grep exited %d\n' "${git_grep_status}" >&2
    exit "${git_grep_status}"
    ;;
esac

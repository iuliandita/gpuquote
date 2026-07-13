#!/usr/bin/env bash
set -euo pipefail

mapfile -d '' go_files < <(find . -type f -name '*.go' -not -path './.git/*' -print0)
unformatted="$(gofmt -l "${go_files[@]}")"
if [[ -n "${unformatted}" ]]; then
  printf 'gofmt required:\n%s\n' "${unformatted}" >&2
  exit 1
fi

./scripts/check-public.sh
go vet ./...
go test -race -count=1 ./...

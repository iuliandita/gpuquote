#!/usr/bin/env bash
set -euo pipefail

go_file_list="$(mktemp)"
cleanup() {
  rm -f "${go_file_list}"
}
trap cleanup EXIT

find . -type f -name '*.go' -not -path './.git/*' -print0 > "${go_file_list}"
mapfile -d '' go_files < "${go_file_list}"
unformatted="$(gofmt -l "${go_files[@]}")"
if [[ -n "${unformatted}" ]]; then
  printf 'gofmt required:\n%s\n' "${unformatted}" >&2
  exit 1
fi

./scripts/check-public.sh
go vet ./...
go test -race -count=1 ./...

#!/usr/bin/env bash
# Copyright Pydantic, Inc. 2025, 2026
# SPDX-License-Identifier: MPL-2.0

set -euo pipefail
cd "$(dirname "$0")/.."

unformatted=$(gofmt -l .)
if [[ -n "$unformatted" ]]; then
  printf 'Run make fmt to format these files:\n%s\n' "$unformatted" >&2
  exit 1
fi

go build ./...
TF_ACC= go test -race -count=1 -timeout=5m ./...

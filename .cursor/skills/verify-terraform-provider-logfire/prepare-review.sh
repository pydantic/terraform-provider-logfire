#!/usr/bin/env bash
# Copyright Pydantic, Inc. 2025, 2026
# SPDX-License-Identifier: MPL-2.0

# Prepares one review bundle per pull request, so independent reviewers start
# from neutral inputs: the exact head, its base, the diff, the author's
# description, one platform API schema and ready tools. It shares no review
# comments, test receipts or conclusions.
#
# Usage: prepare-review.sh PR_NUMBER...
# Set PLATFORM_REPO and PLATFORM_REV to include a platform api.json.

set -euo pipefail
umask 077

if [ "$#" -eq 0 ]; then
  printf 'Usage: %s PR_NUMBER...\n' "$0" >&2
  exit 2
fi
for tool in git gh go curl unzip shasum jq; do
  command -v "$tool" >/dev/null || { printf 'Missing tool: %s\n' "$tool" >&2; exit 1; }
done
if [ -n "${PLATFORM_REPO:-}" ] && [ -z "${PLATFORM_REV:-}" ]; then
  printf 'Set PLATFORM_REV together with PLATFORM_REPO\n' >&2
  exit 2
fi

repo=$(git rev-parse --show-toplevel)
# A short path keeps the provider's Unix socket under the 104-byte macOS limit.
RUN_DIR=$(mktemp -d /tmp/tfr.XXXXXX)
mkdir -p "$RUN_DIR/tools" "$RUN_DIR/cache/go-build" "$RUN_DIR/cache/go-mod" "$RUN_DIR/cache/golangci-lint"
# Every bundle shares these caches. Go locks its caches, so parallel reviews can use them.
export GOCACHE="$RUN_DIR/cache/go-build"
export GOMODCACHE="$RUN_DIR/cache/go-mod"
export GOLANGCI_LINT_CACHE="$RUN_DIR/cache/golangci-lint"

# Every step that creates something adds its removal here. The script finishes the file
# on exit, so a failed run still leaves a working cleanup.
cleanup="$RUN_DIR/cleanup.sh"
printf '#!/usr/bin/env bash\nset -u\n' > "$cleanup"
chmod +x "$cleanup"
finish() {
  local status=$?
  # Go stores modules read-only.
  printf 'chmod -R u+w %q\n' "$RUN_DIR/cache" >> "$cleanup"
  printf 'rm -rf -- %q %q\n' "$RUN_DIR/tools" "$RUN_DIR/cache" >> "$cleanup"
  if [ "$status" -ne 0 ]; then
    printf 'Preparation failed. Cleanup: %s\n' "$cleanup" >&2
  fi
}
trap finish EXIT

install_terraform() {
  local version=$1 os arch archive release dir
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  case $(uname -m) in
    arm64|aarch64) arch=arm64 ;;
    x86_64|amd64) arch=amd64 ;;
    *) printf 'Unsupported Terraform architecture\n' >&2; exit 1 ;;
  esac
  archive="terraform_${version}_${os}_${arch}.zip"
  release="https://releases.hashicorp.com/terraform/$version"
  dir="$RUN_DIR/tools/terraform-$version"
  mkdir -p "$dir"
  curl -fSs "$release/$archive" -o "$dir/$archive"
  curl -fSs "$release/terraform_${version}_SHA256SUMS" -o "$dir/SHA256SUMS"
  (
    cd "$dir"
    awk -v archive="$archive" '$2 == archive' SHA256SUMS > selected.sha256
    test -s selected.sha256
    shasum -a 256 -c selected.sha256 >/dev/null
    unzip -q "$archive"
    rm -- "$archive"
  )
}

# CI's matrix tests these two versions.
install_terraform 1.14.9
install_terraform 1.8.5

mkdir -p "$RUN_DIR/platform"
if [ -n "${PLATFORM_REPO:-}" ]; then
  api_schema=src/services/logfire-backend/logfire_backend/tests/openapi/api.json
  git -C "$PLATFORM_REPO" rev-parse "$PLATFORM_REV^{commit}" > "$RUN_DIR/platform/platform-sha.txt"
  git -C "$PLATFORM_REPO" show "$(cat "$RUN_DIR/platform/platform-sha.txt"):$api_schema" > "$RUN_DIR/platform/api.json"
  shasum -a 256 "$RUN_DIR/platform/api.json" > "$RUN_DIR/platform/api.sha256"
else
  printf 'No platform revision was selected. Record this gap in the review.\n' > "$RUN_DIR/platform/GAP.txt"
fi

for pr in "$@"; do
  dir="$RUN_DIR/pr-$pr"
  mkdir -p "$dir/evidence" "$dir/scratch"
  # One read gives the description, head and base, so they always describe the same push.
  gh pr view "$pr" --json number,url,title,body,baseRefName,headRefOid > "$dir/evidence/pr.json"
  head=$(jq -r .headRefOid "$dir/evidence/pr.json")
  base_ref=$(jq -r .baseRefName "$dir/evidence/pr.json")
  git -C "$repo" fetch -q origin "pull/$pr/head"
  if [ "$(git -C "$repo" rev-parse FETCH_HEAD)" != "$head" ]; then
    printf 'PR %s moved while preparing. Run the script again.\n' "$pr" >&2
    exit 1
  fi
  # Use the fetched commit, since a single-branch clone has no origin/<base> ref for a
  # stacked pull request's base.
  git -C "$repo" fetch -q origin "refs/heads/$base_ref"
  base=$(git -C "$repo" merge-base "$head" FETCH_HEAD)
  printf '%s\n' "$head" > "$dir/evidence/source-sha.txt"
  printf '%s\n' "$base" > "$dir/evidence/base-sha.txt"
  git -C "$repo" diff --binary "$base" "$head" > "$dir/evidence/pr.diff"
  printf 'git -C %q worktree remove --force %q\n' "$repo" "$dir/src" >> "$cleanup"
  git -C "$repo" worktree add -q --detach "$dir/src" "$head"

  cat > "$dir/env.sh" <<EOF
# Source this file before reviewing PR $pr.
export RUN_DIR=$(printf %q "$dir")
export GOCACHE=$(printf %q "$GOCACHE")
export GOMODCACHE=$(printf %q "$GOMODCACHE")
export GOLANGCI_LINT_CACHE=$(printf %q "$GOLANGCI_LINT_CACHE")
export TF_ACC_TERRAFORM_PATH=$(printf %q "$RUN_DIR/tools/terraform-1.14.9/terraform")
# Switch TF_ACC_TERRAFORM_PATH and PATH to this binary to test Terraform 1.8.
export TERRAFORM_1_8=$(printf %q "$RUN_DIR/tools/terraform-1.8.5/terraform")
export PATH=$(printf %q "$RUN_DIR/tools/terraform-1.14.9"):\$PATH
export PLATFORM_DIR=$(printf %q "$RUN_DIR/platform")
export TMPDIR=$(printf %q "$dir/scratch")
export TF_ACC=
cd $(printf %q "$dir/src")
EOF

  # Compiles the provider and its tests once, so a review starts with a warm cache. It never
  # runs a test binary: the PR's init functions and TestMain would run with this environment.
  (
    cd "$dir/src"
    go mod download
    (cd tools && go mod download)
    go build ./...
    mkdir "$dir/scratch/test-bin"
    go test -c -o "$dir/scratch/test-bin/" ./... >/dev/null
    rm -rf -- "$dir/scratch/test-bin"
  )

  base_short=$(git -C "$repo" rev-parse --short "$base")
  head_short=$(git -C "$repo" rev-parse --short "$head")
  cat > "$dir/brief.md" <<EOF
# Review PR $pr

Review pull request $pr at \`$head_short\` against base \`$base_short\`. Find defects in
the change: wrong behavior, failure cases, API mismatches and missing tests.

Inputs:

- \`src/\`: a detached checkout of the head. Do not commit or push from it.
- \`evidence/pr.diff\`: the change against its base.
- \`evidence/pr.json\`: the title and description, which state what the author intends.
- \`$RUN_DIR/platform/\`: the platform API schema and its revision, or the gap when none was selected.
- \`env.sh\`: source it first. It selects Terraform 1.14.9, sets warm Go caches and \`TMPDIR\`, and
  enters \`src/\`. \`TERRAFORM_1_8\` names the Terraform 1.8.5 binary.

Follow the repository's verify-terraform-provider-logfire skill. Its Launch section is
already done. Save commands, exit codes and results in \`evidence/\`. Report each finding
with how to reproduce it.
EOF
done

printf 'Prepared %s\n' "$RUN_DIR"
for pr in "$@"; do
  printf '  PR %s: %s\n' "$pr" "$RUN_DIR/pr-$pr/brief.md"
done
printf 'Cleanup, which keeps each evidence directory: %s\n' "$cleanup"

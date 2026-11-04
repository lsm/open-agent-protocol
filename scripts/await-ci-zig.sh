#!/usr/bin/env bash
set -euo pipefail

poll_seconds="${POLL_SECONDS:-30}"
absent_limit="${ABSENT_LIMIT:-20}"

changed="$(gh api --paginate "repos/${REPO}/pulls/${PR}/files" --jq '.[].filename')"
if [ -n "$changed" ] && ! grep -qvE '^(decisions|drafts|docs|research)/|^[^/]+\.md$' <<<"$changed"; then
  echo "prose only: ci-zig.yml is skipped by its path filter"
  exit 0
fi

own_run='
  [.workflow_runs[]
    | select(any(.pull_requests[]; .number == $pr)
        or (.pull_requests == [] and .head_repository.full_name != $repo))]
  | first
  | if . == null then "absent" else "\(.status) \(.conclusion)" end'

absent=0
while true; do
  state="$(gh api "repos/${REPO}/actions/workflows/ci-zig.yml/runs?head_sha=${HEAD_SHA}&event=pull_request&per_page=100" |
    jq -r --argjson pr "$PR" --arg repo "$REPO" "$own_run")" || state="unreadable"
  case "$state" in
    "completed success")
      echo "ci-zig.yml passed on ${HEAD_SHA}"
      exit 0
      ;;
    completed*)
      echo "ci-zig.yml ended on ${HEAD_SHA}: ${state}" >&2
      exit 1
      ;;
    absent)
      absent=$((absent + 1))
      if [ "$absent" -ge "$absent_limit" ]; then
        echo "a pull request that is not prose only has no ci-zig.yml run on ${HEAD_SHA}" >&2
        exit 1
      fi
      ;;
  esac
  sleep "$poll_seconds"
done

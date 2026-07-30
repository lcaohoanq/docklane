#!/usr/bin/env bash

set -Eeuo pipefail

declare -a dev_pids=()

cleanup() {
  local status=$?
  trap - EXIT INT TERM

  if ((${#dev_pids[@]} > 0)); then
    kill "${dev_pids[@]}" 2>/dev/null || true
    wait "${dev_pids[@]}" 2>/dev/null || true
  fi

  exit "$status"
}

trap cleanup EXIT INT TERM

mkdir -p data .tmp/air

echo "Starting Docklane controller with Air on http://127.0.0.1:4646"
go tool air &
dev_pids+=("$!")

echo "Starting Svelte UI with Vite on http://127.0.0.1:5173"
pnpm --dir web run dev &
dev_pids+=("$!")

wait -n "${dev_pids[@]}"

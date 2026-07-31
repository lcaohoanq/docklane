#!/usr/bin/env bash

set -Eeuo pipefail

declare -a dev_pids=()
declare -a dev_compose=(
  docker compose
  -f docker-compose.yml
  -f docker-compose.dev.yml
)
declare dev_stack_started=false

cleanup() {
  local status=$?
  trap - EXIT INT TERM

  if ((${#dev_pids[@]} > 0)); then
    kill "${dev_pids[@]}" 2>/dev/null || true
    wait "${dev_pids[@]}" 2>/dev/null || true
  fi

  if [[ "$dev_stack_started" == true ]]; then
    echo "Stopping the development controller"
    if ! "${dev_compose[@]}" down; then
      echo "Warning: could not fully stop the development controller" >&2
    fi

    echo "Restoring the integrated controller on http://127.0.0.1:4646"
    if ! docker compose up -d; then
      echo "Error: could not restore the integrated controller" >&2
      status=1
    fi
  fi

  exit "$status"
}

trap cleanup EXIT INT TERM

mkdir -p data

echo "Starting integrated Docklane controller with Air on http://127.0.0.1:4646"
dev_stack_started=true
"${dev_compose[@]}" up -d --force-recreate docklane

"${dev_compose[@]}" logs --follow --no-log-prefix docklane &
dev_pids+=("$!")

echo "Starting Svelte UI with Vite on http://127.0.0.1:5173"
pnpm --dir web run dev &
dev_pids+=("$!")

wait -n "${dev_pids[@]}"

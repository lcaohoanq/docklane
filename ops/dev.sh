#!/usr/bin/env bash

set -Eeuo pipefail

declare -a dev_pids=()
declare -a dev_compose=()
declare -a integrated_compose=()
declare dev_stack_started=false
declare integrated_controller_removed=false

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"

integrated_project="$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project" }}' docklane 2>/dev/null || true)"
integrated_working_dir="$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}' docklane 2>/dev/null || true)"
integrated_config_files="$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project.config_files" }}' docklane 2>/dev/null || true)"
integrated_running="$(docker inspect --format '{{ .State.Running }}' docklane 2>/dev/null || true)"
probe_running="$(docker inspect --format '{{ .State.Running }}' docklane-probe 2>/dev/null || true)"

if [[ -z "$integrated_project" || -z "$integrated_working_dir" || -z "$integrated_config_files" ]]; then
  echo "Error: an integrated Compose-managed 'docklane' controller must be running before make dev" >&2
  exit 1
fi
if [[ "$integrated_running" != true ]]; then
  echo "Error: the integrated 'docklane' controller is not running" >&2
  exit 1
fi
if [[ "$probe_running" != true ]]; then
  echo "Error: the integrated 'docklane-probe' container is not running" >&2
  exit 1
fi
if [[ ! -d "$integrated_working_dir/data" ]]; then
  echo "Error: integrated Docklane data directory is missing: $integrated_working_dir/data" >&2
  exit 1
fi

integrated_compose=(docker compose -p "$integrated_project")
IFS=',' read -r -a integrated_files <<< "$integrated_config_files"
for config_file in "${integrated_files[@]}"; do
  integrated_compose+=(-f "$config_file")
done

dev_compose=(
  env "DOCKLANE_DATA_DIR=$integrated_working_dir/data"
  docker compose
  -f docker-compose.yml
  -f docker-compose.dev.yml
)

cleanup() {
  local status=$?
  trap - EXIT INT TERM

  if ((${#dev_pids[@]} > 0)); then
    kill "${dev_pids[@]}" 2>/dev/null || true
    wait "${dev_pids[@]}" 2>/dev/null || true
  fi

  if [[ "$dev_stack_started" == true ]]; then
    echo "Stopping the development controller"
    if ! "${dev_compose[@]}" rm --stop --force docklane; then
      echo "Warning: could not fully stop the development controller" >&2
    fi
  fi

  if [[ "$integrated_controller_removed" == true ]]; then
    echo "Restoring the integrated controller on http://127.0.0.1:4646"
    if ! "${integrated_compose[@]}" up -d --no-deps docklane; then
      echo "Error: could not restore the integrated controller" >&2
      status=1
    fi
  fi

  exit "$status"
}

trap cleanup EXIT INT TERM

echo "Pausing integrated controller from $integrated_working_dir"
integrated_controller_removed=true
"${integrated_compose[@]}" rm --stop --force docklane

echo "Starting integrated Docklane controller with Air on http://127.0.0.1:4646"
dev_stack_started=true
"${dev_compose[@]}" up -d --force-recreate --no-deps docklane

"${dev_compose[@]}" logs --follow --no-log-prefix docklane &
dev_pids+=("$!")

echo "Starting Svelte UI with Vite on http://127.0.0.1:5173"
pnpm --dir web run dev &
dev_pids+=("$!")

wait -n "${dev_pids[@]}"

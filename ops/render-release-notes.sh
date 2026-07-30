#!/usr/bin/env bash
set -euo pipefail

source_file="${1:?usage: render-release-notes.sh SOURCE_FILE}"

test -f "$source_file"
test "$(sed -n '1p' "$source_file")" = "---"

awk '
  NR == 1 && $0 == "---" {
    in_frontmatter = 1
    next
  }
  in_frontmatter && $0 == "---" {
    in_frontmatter = 0
    rendered = 1
    next
  }
  rendered {
    print
  }
' "$source_file"

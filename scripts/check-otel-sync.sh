#!/usr/bin/env bash
# Fail if the text forge emits in its Runtime Tracing section has drifted from
# the agent-reliability-otel-labels repo it describes.
#
# Background: that text (internal/forge/tracingtext/) is a second copy of what
# agent-reliability-otel-labels documents. `go test` already checks it against a vendored
# copy of the library's key manifest; this script checks the vendored copy and
# the function names against the real thing.
#
# Checks two things:
#   1. testdata/forge/otel/otel-spec-keys.json equals <otel>/spec/keys.json
#      (line endings ignored).
#   2. Every identifier in testdata/forge/otel/api-names.txt is still declared
#      in that language's binding source. Only a declaration counts (func,
#      method, type, const, var, class, def, class member), and only in source
#      files (*.py, *.ts, non-test *.go): a name left in a comment, a call to
#      another package's function of the same name (rand.Read), or a stale
#      __pycache__/*.pyc does not hide a rename.
#
# Not covered: a third-party instrumentor renaming a package or call.
#
# Usage:
#   OTEL_REPO=../agent-reliability-otel-labels scripts/check-otel-sync.sh
#   scripts/check-otel-sync.sh                 # defaults to ../agent-reliability-otel-labels
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
otel="${OTEL_REPO:-$repo_root/../agent-reliability-otel-labels}"
vendored="$repo_root/testdata/forge/otel/otel-spec-keys.json"
names="$repo_root/testdata/forge/otel/api-names.txt"

if [ ! -d "$otel" ]; then
  echo "error: agent-reliability-otel-labels repo not found at '$otel' (set OTEL_REPO)" >&2
  exit 2
fi

status=0

if ! diff -q <(tr -d '\r' < "$vendored") <(tr -d '\r' < "$otel/spec/keys.json") >/dev/null 2>&1; then
  status=1
  echo "KEYS DRIFT: testdata/forge/otel/otel-spec-keys.json != agent-reliability-otel-labels spec/keys.json" >&2
  echo "  copy the new file in, then fix internal/forge/tracingtext/ until go test passes" >&2
fi

# declared_go NAME: a top-level func or method, a type, a single const/var, or
# an entry of a const (...) / var (...) block, in a non-test .go file.
declared_go() {
  local f
  for f in "$otel"/go/*.go; do
    case "$f" in *_test.go) continue ;; esac
    awk -v n="$1" '
      /^(const|var)[[:space:]]*\($/ { block = 1; next }
      block && /^\)/                { block = 0; next }
      block && $1 == n               { found = 1 }
      $0 ~ "^func[[:space:]]+(\\([^)]*\\)[[:space:]]*)?" n "[[:space:]]*[[(]" { found = 1 }
      $0 ~ "^(type|const|var)[[:space:]]+" n "([^A-Za-z0-9_]|$)"              { found = 1 }
      END { exit !found }
    ' "$f" && return 0
  done
  return 1
}

# declared_py NAME: a def or class at any depth, a module- or class-level
# assignment, or an instance attribute set on self, in a .py file.
declared_py() {
  grep -rqE --include='*.py' \
    -e "^[[:space:]]*(async[[:space:]]+)?def[[:space:]]+$1[[:space:]]*\(" \
    -e "^[[:space:]]*class[[:space:]]+$1([^A-Za-z0-9_]|\$)" \
    -e "^( {4})?$1[[:space:]]*(:[^=]*)?=" \
    -e "self\.$1[[:space:]]*(:[^=]*)?=" \
    -- "$otel/python/src"
}

# declared_ts NAME: a top-level declaration (exported or not), or a class
# member at the first indent level, in a .ts file.
declared_ts() {
  grep -rqE --include='*.ts' \
    -e "^(export[[:space:]]+)?(declare[[:space:]]+)?(default[[:space:]]+)?(async[[:space:]]+)?(function\*?|class|interface|type|enum|const|let|var)[[:space:]]+$1([^A-Za-z0-9_]|\$)" \
    -e "^  ((public|private|protected|readonly|static|async|get|set|override)[[:space:]]+)*$1[[:space:]]*[(<:=;!?]" \
    -- "$otel/typescript/src"
}

count=0
while IFS=$'\t' read -r lang name; do
  [ -z "$lang" ] && continue
  count=$((count + 1))
  case "$lang" in
    python)     declared_py "$name" && hit=yes || hit="" ;;
    typescript) declared_ts "$name" && hit=yes || hit="" ;;
    go)         declared_go "$name" && hit=yes || hit="" ;;
    *)
      status=1
      echo "UNKNOWN LANGUAGE '$lang' in testdata/forge/otel/api-names.txt" >&2
      continue
      ;;
  esac
  if [ -z "$hit" ]; then
    status=1
    echo "API DRIFT: the $lang binding no longer defines '$name'" >&2
  fi
done < "$names"

if [ "$count" -eq 0 ]; then
  echo "error: testdata/forge/otel/api-names.txt is empty; nothing was checked" >&2
  exit 2
fi

if [ "$status" -ne 0 ]; then
  echo "" >&2
  echo "forge's Runtime Tracing text is out of sync with agent-reliability-otel-labels." >&2
  exit 1
fi

echo "forge tracing text is in sync with agent-reliability-otel-labels ($count identifiers checked)"

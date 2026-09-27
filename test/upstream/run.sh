#!/usr/bin/env bash
# Runs Chroma's own Python test suite (pinned to the release Kaleid targets)
# against a running Kaleid server, using Chroma's integration-test mode.
#
#   KALEID_HOST=localhost KALEID_PORT=8000 test/upstream/run.sh <pytest args>
#
# The server must run with KALEID_ALLOW_RESET=true: the suite resets the
# database between tests.
set -euo pipefail

CHROMA_VERSION="${CHROMA_VERSION:-1.5.9}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="${UPSTREAM_WORKDIR:-$HERE/.work}"
PYTHON="${PYTHON:-python3}"

mkdir -p "$WORK"
if [ ! -x "$WORK/venv/bin/python" ]; then
  "$PYTHON" -m venv "$WORK/venv"
  "$WORK/venv/bin/pip" install -q --upgrade pip
  "$WORK/venv/bin/pip" install -q -r "$HERE/requirements.txt"
fi
PY="$WORK/venv/bin/python"

PKG="$("$PY" -c 'import chromadb, os; print(os.path.dirname(chromadb.__file__))')"
if [ ! -f "$PKG/test/.kaleid-$CHROMA_VERSION" ]; then
  rm -rf "$WORK/chroma"
  git clone -q --depth 1 --branch "$CHROMA_VERSION" --filter=blob:none --sparse \
    https://github.com/chroma-core/chroma.git "$WORK/chroma"
  git -C "$WORK/chroma" sparse-checkout set chromadb/test
  rm -rf "$PKG/test"
  cp -r "$WORK/chroma/chromadb/test" "$PKG/test"
  touch "$PKG/test/.kaleid-$CHROMA_VERSION"
fi

DESELECT=()
while IFS= read -r line; do
  line="${line%%#*}"
  line="$(echo "$line" | xargs)"
  [ -n "$line" ] && DESELECT+=(--deselect "$line")
done < "$HERE/deselect.txt"

cd "$(dirname "$PKG")"
export CHROMA_INTEGRATION_TEST_ONLY=1
export CHROMA_SERVER_HOST="${KALEID_HOST:-localhost}"
export CHROMA_SERVER_HTTP_PORT="${KALEID_PORT:-8000}"
export PROPERTY_TESTING_PRESET="${PROPERTY_TESTING_PRESET:-fast}"
exec "$PY" -m pytest -p no:cacheprovider -q -rfE "${DESELECT[@]}" "$@"

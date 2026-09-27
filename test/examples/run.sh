#!/usr/bin/env bash
# Runs every example and sample app against a live server, offline (hashing
# embedder, no LLM), failing on the first error.
#
#   KALEID_URL=http://localhost:8000 [KALEID_AUTH_URL=http://localhost:8002 KALEID_TOKEN=...] test/examples/run.sh
#
# KALEID_AUTH_URL, if set, is a second server started with token auth
# (KALEID_TOKEN) and is used for the auth example.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
export KALEID_URL="${KALEID_URL:-http://localhost:8000}"
export EMBEDDER=hash

run() { echo "::group::$*"; "$@"; echo "::endgroup::"; }

for ex in start_here where_filtering embeddings forking hybrid_search; do
  run go run "./examples/$ex"
done
run go run ./examples/chat_with_your_documents load -reset
run go run ./examples/chat_with_your_documents chat -no-llm -question "What did the president say about inflation?"
if [ -n "${KALEID_AUTH_URL:-}" ]; then
  KALEID_URL="$KALEID_AUTH_URL" run go run ./examples/auth
fi
run go run ./sample_apps/movies load
run go test -count=1 ./sample_apps/...
( cd sample_apps/generative_benchmarking && out="$(mktemp -d)" &&
  run go run . generate -heuristic -out "$out/queries.json" &&
  run go run . evaluate -queries "$out/queries.json" -results "$out/results" &&
  run go run . compare "$out"/results/*.json )
echo "all examples ran"

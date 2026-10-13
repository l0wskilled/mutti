#!/usr/bin/env bash
# Runs unit and integration tests with coverage, merges both and fails below 100% for mutti's
# own code. Test support packages (*test) and the main wiring are not counted.
set -euo pipefail

cd "$(dirname "$0")/.."
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
mkdir -p "$out/unit" "$out/integration"

pkgs=github.com/l0wskilled/mutti/...
go test -race -coverpkg="$pkgs" ./... -args -test.gocoverdir="$out/unit" >/dev/null
go test -race -tags integration -coverpkg="$pkgs" ./... -args -test.gocoverdir="$out/integration" >/dev/null

go tool covdata textfmt -i="$out/unit,$out/integration" -o "$out/all.out"
grep -vE '^github.com/l0wskilled/mutti/(cmd/|.*test/)' "$out/all.out" >"$out/own.out"
cp "$out/own.out" coverage.out

total=$(go tool cover -func="$out/own.out" | awk '/^total:/ {print $3}')
if [[ "$total" != "100.0%" ]]; then
	echo "coverage $total, want 100.0%. Uncovered:" >&2
	go tool cover -func="$out/own.out" | grep -v '100.0%' >&2
	exit 1
fi
echo "coverage 100.0%"

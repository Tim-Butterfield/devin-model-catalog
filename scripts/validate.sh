#!/usr/bin/env bash
# Full local validation: formatting, module hygiene, vet, tests, race detector,
# cross-compilation and tagged-build vet. Every check runs even when an earlier
# one fails; the script exits non-zero if any check failed.
# Deterministic: no network access beyond Go module downloads. Checks against
# live sources or a real browser are separate:
#   go test ./internal/app -run TestLiveRefresh -tags=live -v
#   go test ./internal/retrieval/browser -run TestRealBrowserRuntime -tags=browser -v
set -uo pipefail

cd "$(dirname "$0")/.."
failed=0
step() { printf '\n==> %s\n' "$*"; }
check() {
	if "$@"; then
		echo "ok"
	else
		echo "FAILED: $*"
		failed=1
	fi
}

step "gofmt"
# gofmt -l lists unformatted files, but on a file it cannot parse it prints
# nothing to stdout and exits non-zero; both must fail validation.
files=$(git ls-files '*.go' 2>/dev/null) || files=$(find . -name '*.go' -not -path './.git/*')
if ! unformatted=$(gofmt -l $files); then
	echo "FAILED: gofmt could not check every file"
	failed=1
elif [ -n "$unformatted" ]; then
	echo "unformatted files:"
	echo "$unformatted"
	failed=1
else
	echo "ok"
fi

step "go mod verify"
check go mod verify

step "go mod tidy"
# -diff prints what tidy would change without modifying go.mod or go.sum, and
# exits non-zero both when they are not tidy and when tidy itself fails.
check go mod tidy -diff

step "go vet"
check go vet ./...

step "go test"
check go test -count=1 ./...

step "go test -race"
check go test -count=1 -race ./...

step "cross-compile (CGO_ENABLED=0)"
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
	goos=${target%/*}
	goarch=${target#*/}
	if CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -o /dev/null ./cmd/devmodels; then
		echo "ok   $target"
	else
		echo "FAIL $target"
		failed=1
	fi
done

step "live- and browser-tagged tests compile"
check go vet -tags=live ./internal/app
check go vet -tags=browser ./internal/retrieval/browser

if [ "$failed" -ne 0 ]; then
	printf '\nVALIDATION FAILED\n'
	exit 1
fi
printf '\nVALIDATION PASSED\n'

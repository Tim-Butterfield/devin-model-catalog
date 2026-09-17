#!/usr/bin/env bash
# Regression tests for scripts/validate.sh itself. The go and gofmt commands are
# replaced by stubs on PATH, so the real toolchain and module files are never
# touched and no Go check actually runs.
set -uo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
stubs=$(mktemp -d)
trap 'rm -rf "$stubs"' EXIT

# A stub fails when its command line contains one of the '|'-separated patterns
# in STUB_FAIL, and succeeds silently otherwise.
for tool in go gofmt; do
	cat > "$stubs/$tool" <<'STUB'
#!/usr/bin/env bash
command_line="$(basename "$0") $*"
IFS='|' read -r -a patterns <<< "${STUB_FAIL:-}"
for pattern in "${patterns[@]}"; do
	if [ -n "$pattern" ] && [[ "$command_line" == *"$pattern"* ]]; then
		echo "stub: $command_line failed" >&2
		exit 1
	fi
done
exit 0
STUB
	chmod +x "$stubs/$tool"
done

failures=0
# expect NAME STUB_FAIL WANT_EXIT TEXT... runs validate.sh and checks its exit
# code and that its output contains every TEXT.
expect() {
	local name=$1 fail=$2 want=$3
	shift 3
	local out code ok=1
	out=$(STUB_FAIL=$fail PATH="$stubs:$PATH" "$root/scripts/validate.sh" 2>&1)
	code=$?
	[ "$code" -eq "$want" ] || ok=0
	for text in "$@"; do
		[[ "$out" == *"$text"* ]] || ok=0
	done
	if [ "$ok" -eq 1 ]; then
		echo "ok   $name"
	else
		echo "FAIL $name (exit $code, want $want)"
		printf '%s\n' "$out" | sed 's/^/     /'
		failures=1
	fi
}

expect "every check passes" "" 0 "VALIDATION PASSED"
expect "a failing go mod tidy fails validation" "go mod tidy" 1 "FAILED: go mod tidy -diff" "VALIDATION FAILED"
expect "a file gofmt cannot parse fails validation" "gofmt -l" 1 "FAILED: gofmt could not check every file" "VALIDATION FAILED"
expect "a failed check does not stop later checks" "go vet ./..." 1 "FAILED: go vet ./..." "ok   windows/arm64" "VALIDATION FAILED"

exit "$failures"

#!/usr/bin/env bash
# Structural checks on the GitHub Actions workflows.
#
# These assert the properties that matter if the files are edited later and the
# reasoning behind them is forgotten: that the job which executes repository
# code cannot publish anything, that the job which can publish never executes
# repository code, that artifacts are verified on both sides of the handoff, and
# that every action stays pinned to an immutable commit.
#
# Deliberately plain text matching rather than a YAML toolchain: these are
# grep-shaped questions, and the checks must run anywhere bash does, with no
# extra dependency to install.
set -uo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
ci="$root/.github/workflows/ci.yml"
release="$root/.github/workflows/release.yml"

failures=0
pass() { echo "ok   $1"; }
fail() {
	echo "FAIL $1"
	shift
	printf '     %s\n' "$@"
	failures=1
}

# job_block <file> <name> prints the lines of one job, from its "  name:" header
# to the next line indented two spaces (the next job).
job_block() {
	awk -v want="  $2:" '
		$0 == want { inside = 1; next }
		inside && /^  [A-Za-z_-]+:/ { exit }
		inside { print }
	' "$1"
}

has() { printf '%s\n' "$2" | grep -qE "$1"; }

# line_of <pattern> <text> prints the 1-based line number of the first match.
line_of() { printf '%s\n' "$2" | grep -nE "$1" | head -1 | cut -d: -f1; }

# ------------------------------------------------------------------ jobs exist

build=$(job_block "$release" build)
publish=$(job_block "$release" publish)

if [ -n "$build" ] && [ -n "$publish" ]; then
	pass "release has separate build and publish jobs"
else
	fail "release has separate build and publish jobs" "one of them was not found"
fi

if has '^ *needs: build' "$publish"; then
	pass "publish depends on build"
else
	fail "publish depends on build" "no 'needs: build' in the publish job"
fi

# ------------------------------------------------------- build job privileges

if has '^ *contents: read' "$build"; then
	pass "build job is contents: read"
else
	fail "build job is contents: read" "it does not declare read-only contents"
fi

for forbidden in 'contents: write' 'id-token: write' 'attestations: write'; do
	if has "$forbidden" "$build"; then
		fail "build job has no $forbidden" "it grants $forbidden"
	else
		pass "build job has no $forbidden"
	fi
done

# The build job must not be able to publish or attest even if it had the token.
for forbidden in 'gh release create' 'actions/attest@'; do
	if has "$forbidden" "$build"; then
		fail "build job does not $forbidden" "found it in the build job"
	else
		pass "build job does not $forbidden"
	fi
done

# ----------------------------------------------------- publish job privileges

for required in 'contents: write' 'id-token: write' 'attestations: write'; do
	if has "$required" "$publish"; then
		pass "publish job grants $required"
	else
		fail "publish job grants $required" "it is missing"
	fi
done

# Exactly those three, and nothing else.
granted=$(printf '%s\n' "$publish" | awk '
	/^    permissions:/ { inside = 1; next }
	inside && /^    [a-z-]+:/ { exit }
	inside && /^      [a-z-]+: / { print $1 }
' | tr -d ':' | LC_ALL=C sort | tr '\n' ' ')
if [ "$granted" = "attestations contents id-token " ]; then
	pass "publish job grants only the three intended permissions"
else
	fail "publish job grants only the three intended permissions" "granted: $granted"
fi

# Publication and attestation happen only there.
if has 'gh release create' "$publish"; then
	pass "publication happens in the publish job"
else
	fail "publication happens in the publish job" "no 'gh release create' found"
fi
if has 'actions/attest@' "$publish"; then
	pass "attestation happens in the publish job"
else
	fail "attestation happens in the publish job" "no attest action found"
fi

# The publish job must not check out or run the release script.
for forbidden in 'actions/checkout@' 'scripts/release.sh'; do
	if has "$forbidden" "$publish"; then
		fail "publish job does not use $forbidden" "found it, so it executes repository code"
	else
		pass "publish job does not use $forbidden"
	fi
done

# ------------------------------------------------------------ artifact handoff

if has 'actions/upload-artifact@' "$build"; then
	pass "build job uploads the payload"
else
	fail "build job uploads the payload" "no upload-artifact step"
fi
if has 'actions/download-artifact@' "$publish"; then
	pass "publish job downloads the payload"
else
	fail "publish job downloads the payload" "no download-artifact step"
fi

# The upload must come after the build job verified what it produced.
verify_line=$(line_of 'sha256sum -c SHA256SUMS' "$build")
upload_line=$(line_of 'actions/upload-artifact@' "$build")
if [ -n "$verify_line" ] && [ -n "$upload_line" ] && [ "$verify_line" -lt "$upload_line" ]; then
	pass "build job verifies checksums before uploading"
else
	fail "build job verifies checksums before uploading" "verify at $verify_line, upload at $upload_line"
fi

# The publish job must re-check the payload after it arrives.
download_line=$(line_of 'actions/download-artifact@' "$publish")
inventory_line=$(line_of 'Verify the artifact inventory' "$publish")
recheck_line=$(line_of 'sha256sum -c SHA256SUMS' "$publish")
attest_line=$(line_of 'actions/attest@' "$publish")
if [ -n "$download_line" ] && [ -n "$recheck_line" ] && [ "$download_line" -lt "$recheck_line" ]; then
	pass "publish job re-verifies checksums after download"
else
	fail "publish job re-verifies checksums after download" "download at $download_line, verify at $recheck_line"
fi
if [ -n "$inventory_line" ] && [ "$download_line" -lt "$inventory_line" ] && [ "$inventory_line" -lt "$attest_line" ]; then
	pass "publish job checks the inventory between download and attestation"
else
	fail "publish job checks the inventory between download and attestation" \
		"download $download_line, inventory $inventory_line, attest $attest_line"
fi
if [ -n "$recheck_line" ] && [ "$recheck_line" -lt "$attest_line" ]; then
	pass "nothing is attested before the checksums re-verify"
else
	fail "nothing is attested before the checksums re-verify" "verify at $recheck_line, attest at $attest_line"
fi

# ------------------------------------------------------------------- CI is read-only

for forbidden in 'contents: write' 'id-token: write' 'attestations: write' 'gh release create' 'actions/attest@'; do
	if grep -qE "$forbidden" "$ci"; then
		fail "ordinary CI has no $forbidden" "found it in ci.yml"
	else
		pass "ordinary CI has no $forbidden"
	fi
done
if grep -qE '^permissions:' "$ci" && grep -qE '^ *contents: read' "$ci"; then
	pass "ordinary CI declares read-only contents"
else
	fail "ordinary CI declares read-only contents" "no top-level read-only permissions"
fi

# --------------------------------------------------------------- pins and safety

for wf in "$ci" "$release"; do
	name=$(basename "$wf")

	# Every action reference is a full 40-character commit SHA.
	while read -r ref; do
		[ -n "$ref" ] || continue
		if printf '%s' "$ref" | grep -qE '@[0-9a-f]{40}$'; then
			pass "$name: $ref is a full SHA pin"
		else
			fail "$name: $ref is a full SHA pin" "it is not pinned to a 40-hex commit"
		fi
	done < <(grep -hoE 'uses: [^ ]+' "$wf" | sed 's/^uses: //')

	if grep -qE 'uses: .*@v[0-9]+ *$' "$wf"; then
		fail "$name has no mutable @vN reference" "found one"
	else
		pass "$name has no mutable @vN reference"
	fi

	if grep -q 'pull_request_target' "$wf"; then
		fail "$name does not use pull_request_target" "found it"
	else
		pass "$name does not use pull_request_target"
	fi

	# Every checkout disables credential persistence.
	checkouts=$(grep -c 'actions/checkout@' "$wf")
	persists=$(grep -c 'persist-credentials: false' "$wf")
	if [ "$checkouts" -eq "$persists" ]; then
		pass "$name: all $checkouts checkout step(s) set persist-credentials: false"
	else
		fail "$name: all checkout steps set persist-credentials: false" \
			"$checkouts checkout(s) but $persists persist-credentials: false"
	fi

	# No expression interpolation inside a run: body. Values reach the shell
	# through env:, so a tag or branch name cannot be spliced into a command.
	interpolated=$(awk '
		/^ *run: *\|/  { inrun = 1; indent = match($0, /[^ ]/); next }
		/^ *run: /     { if ($0 ~ /\$\{\{/) print NR ": " $0; next }
		inrun && /[^ ]/ && match($0, /[^ ]/) <= indent { inrun = 0 }
		inrun && /\$\{\{/ { print NR ": " $0 }
	' "$wf")
	if [ -z "$interpolated" ]; then
		pass "$name has no \${{ }} interpolation inside a run: body"
	else
		fail "$name has no \${{ }} interpolation inside a run: body" "$interpolated"
	fi
done

exit "$failures"

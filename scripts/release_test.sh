#!/usr/bin/env bash
# Regression tests for scripts/release.sh. The go command is replaced by a stub
# on PATH that records its arguments and writes a placeholder executable, so the
# packaging, naming, checksum and version-validation logic is exercised without
# a real cross-compile and without the network.
#
# What a real build produces is checked separately, by building and running the
# native binary; this file checks the parts that must hold for all six targets.
set -uo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

stubs="$work/stubs"
mkdir -p "$stubs"
cat >"$stubs/go" <<'STUB'
#!/usr/bin/env bash
# Record the invocation, then write a placeholder where -o points.
printf 'CGO_ENABLED=%s GOOS=%s GOARCH=%s %s\n' "${CGO_ENABLED-}" "${GOOS-}" "${GOARCH-}" "$*" >>"$GO_STUB_LOG"
out=""
prev=""
for arg in "$@"; do
	[ "$prev" = "-o" ] && out=$arg
	prev=$arg
done
[ -n "$out" ] && printf 'placeholder binary\n' >"$out"
exit 0
STUB
chmod +x "$stubs/go"

failures=0
pass() { echo "ok   $1"; }
fail() {
	echo "FAIL $1"
	shift
	printf '     %s\n' "$@"
	failures=1
}

# ---------------------------------------------------------------- version syntax

for good in v0.1.0 v1.0.0 v10.20.30 v1.0.0-rc.1 v1.2.3-alpha.1 v1.2.3+build.5 \
	v1.0.0-0 v1.0.0-alpha.0 v1.0.0-alpha v1.0.0-alpha-beta v1.0.0-x.7.z.92 \
	v1.0.0-0A v1.0.0-00a v1.0.0+001 v1.0.0+20130313144700 v1.0.0-beta+exp.sha.5114f85 \
	v0.0.4 v1.1.2-prerelease+meta; do
	if "$root/scripts/release.sh" --check-version "$good" 2>/dev/null; then
		pass "accepts $good"
	else
		fail "accepts $good" "a valid SemVer version was rejected"
	fi
done

# A *numeric* prerelease identifier may not carry a leading zero. An
# alphanumeric one may ("00a" above), and build metadata may ("+001" above);
# these are the cases a single regex most often gets wrong.
for bad in 0.1.0 v1 v1.2 v1.2.3.4 v01.2.3 v1.02.3 v1.2.03 vx.y.z "v1.2.3 " "" \
	"v-1.2.3" "1.2.3-rc1" "release-1.2.3" v1.0.0-01 v1.0.0-alpha.01 v1.0.0-rc.000 \
	"v1.0.0-alpha..1" "v1.0.0-.alpha" "v1.0.0-alpha." "v1.0.0-" "v1.0.0+" \
	"v1.0.0-alpha+" "v1.0.0+build..1" "v1.0.0+build." "v1.0.0-alpha_1" "v1.0.0-α" \
	v1.0.0-0.01 v1.0.0-1.2.03; do
	if "$root/scripts/release.sh" --check-version "$bad" 2>/dev/null; then
		fail "rejects '$bad'" "a malformed version was accepted"
	else
		pass "rejects '$bad'"
	fi
done

# A version is required: no argument must not silently build something.
if "$root/scripts/release.sh" >/dev/null 2>&1; then
	fail "requires a version" "running with no argument succeeded"
else
	pass "requires a version"
fi

# ---------------------------------------------------------------- release notes

# A release publishes the CHANGELOG section for its version, so a tag with no
# section must fail rather than publish an empty release.
notes=$("$root/scripts/release.sh" --notes v0.1.0 2>/dev/null)
if [ -n "$notes" ] && printf '%s' "$notes" | grep -q 'First public release'; then
	pass "extracts the CHANGELOG section for a released version"
else
	fail "extracts the CHANGELOG section for a released version" "got: $notes"
fi

# The next section's heading must not bleed into the extracted notes.
if printf '%s' "$notes" | grep -q '^## '; then
	fail "notes stop at the next version heading" "the extract contains a '## ' heading"
else
	pass "notes stop at the next version heading"
fi

if "$root/scripts/release.sh" --notes v99.99.99 >/dev/null 2>&1; then
	fail "refuses a version with no CHANGELOG section" "it succeeded"
else
	pass "refuses a version with no CHANGELOG section"
fi

if "$root/scripts/release.sh" --notes not-a-version >/dev/null 2>&1; then
	fail "notes validate the version too" "a malformed version was accepted"
else
	pass "notes validate the version too"
fi

# ---------------------------------------------------------------- target matrix

expected_targets="darwin/arm64
darwin/amd64
linux/amd64
linux/arm64
windows/amd64
windows/arm64"

actual_targets=$("$root/scripts/release.sh" --targets)
if [ "$actual_targets" = "$expected_targets" ]; then
	pass "target matrix is the six settled targets"
else
	fail "target matrix is the six settled targets" "got:" "$actual_targets"
fi

# ---------------------------------------------------------------- packaging run

export GO_STUB_LOG="$work/go.log"
: >"$GO_STUB_LOG"

build_out=$(cd "$root" && PATH="$stubs:$PATH" scripts/release.sh v9.9.9 2>&1)
build_code=$?
if [ "$build_code" -ne 0 ]; then
	fail "packaging run succeeds" "exit $build_code" "$build_out"
fi

dist="$root/dist"

# Every target is built, with the flags a release requires.
for target in $expected_targets; do
	goos=${target%/*}
	goarch=${target#*/}
	if grep -q "GOOS=$goos GOARCH=$goarch " "$GO_STUB_LOG"; then
		pass "builds $target"
	else
		fail "builds $target" "no go invocation recorded for it"
	fi
done

if [ "$(grep -c . "$GO_STUB_LOG")" -eq 6 ]; then
	pass "builds exactly six targets"
else
	fail "builds exactly six targets" "recorded $(grep -c . "$GO_STUB_LOG") invocations"
fi

for required in "CGO_ENABLED=0" "-trimpath" "-buildvcs=false" \
	"buildinfo.Version=v9.9.9"; do
	missing=$(grep -cv -- "$required" "$GO_STUB_LOG")
	if [ "$missing" -eq 0 ]; then
		pass "every build passes $required"
	else
		fail "every build passes $required" "$missing of 6 invocations lacked it"
	fi
done

# ---------------------------------------------------------------- artifact names

expected_archives="devmodels_9.9.9_darwin_amd64.tar.gz
devmodels_9.9.9_darwin_arm64.tar.gz
devmodels_9.9.9_linux_amd64.tar.gz
devmodels_9.9.9_linux_arm64.tar.gz
devmodels_9.9.9_windows_amd64.zip
devmodels_9.9.9_windows_arm64.zip"

actual_archives=$(cd "$dist" && ls -1 ./*.tar.gz ./*.zip 2>/dev/null | sed 's|^\./||' | LC_ALL=C sort)
if [ "$actual_archives" = "$expected_archives" ]; then
	pass "archive names and formats are exactly the six expected"
else
	fail "archive names and formats are exactly the six expected" "got:" "$actual_archives"
fi

# The version in a filename carries no leading v.
if ls "$dist" | grep -q 'devmodels_v'; then
	fail "archive names drop the leading v" "a name still contains devmodels_v"
else
	pass "archive names drop the leading v"
fi

if [ "$(cd "$dist" && ls -1 ./*.tar.gz ./*.zip | sort | uniq -d | wc -l | tr -d ' ')" = "0" ]; then
	pass "no duplicate artifact names"
else
	fail "no duplicate artifact names" "two targets produced the same filename"
fi

# ---------------------------------------------------------------- archive contents

for f in "$dist"/*.tar.gz; do
	contents=$(tar tzf "$f" | LC_ALL=C sort | tr '\n' ' ')
	if [ "$contents" = "LICENSE devmodels " ]; then
		pass "$(basename "$f") contains only the executable and LICENSE"
	else
		fail "$(basename "$f") contains only the executable and LICENSE" "got: $contents"
	fi
done

for f in "$dist"/*.zip; do
	contents=$(unzip -Z1 "$f" | LC_ALL=C sort | tr '\n' ' ')
	if [ "$contents" = "LICENSE devmodels.exe " ]; then
		pass "$(basename "$f") contains only devmodels.exe and LICENSE"
	else
		fail "$(basename "$f") contains only devmodels.exe and LICENSE" "got: $contents"
	fi
done

# No archive may carry an absolute path or an escape.
for f in "$dist"/*.tar.gz; do
	if tar tzf "$f" | grep -qE '^/|\.\./'; then
		fail "$(basename "$f") has no absolute or traversing paths" "found one"
	else
		pass "$(basename "$f") has no absolute or traversing paths"
	fi
done
for f in "$dist"/*.zip; do
	if unzip -Z1 "$f" | grep -qE '^/|\.\./'; then
		fail "$(basename "$f") has no absolute or traversing paths" "found one"
	else
		pass "$(basename "$f") has no absolute or traversing paths"
	fi
done

# ---------------------------------------------------------------- checksums

if [ -f "$dist/SHA256SUMS" ]; then
	pass "SHA256SUMS exists"
else
	fail "SHA256SUMS exists" "it was not written"
fi

sums_listed=$(awk '{print $NF}' "$dist/SHA256SUMS" | LC_ALL=C sort)
if [ "$sums_listed" = "$expected_archives" ]; then
	pass "SHA256SUMS covers every archive and nothing else"
else
	fail "SHA256SUMS covers every archive and nothing else" "got:" "$sums_listed"
fi

if (cd "$dist" && { command -v sha256sum >/dev/null 2>&1 && sha256sum -c SHA256SUMS >/dev/null 2>&1 || shasum -a 256 -c SHA256SUMS >/dev/null 2>&1; }); then
	pass "SHA256SUMS verifies"
else
	fail "SHA256SUMS verifies" "verification failed"
fi

# SHA256SUMS must not list itself.
if grep -q 'SHA256SUMS' "$dist/SHA256SUMS"; then
	fail "SHA256SUMS does not list itself" "it does"
else
	pass "SHA256SUMS does not list itself"
fi

# ---------------------------------------------------------------- no stray files

stray=$(cd "$dist" && ls -1A | grep -v -E '^(devmodels_.*\.(tar\.gz|zip)|SHA256SUMS)$' || true)
if [ -z "$stray" ]; then
	pass "dist/ holds only archives and SHA256SUMS"
else
	fail "dist/ holds only archives and SHA256SUMS" "stray:" "$stray"
fi

rm -rf "$dist"
exit "$failures"

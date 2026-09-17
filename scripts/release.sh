#!/usr/bin/env bash
# Build and package the release artifacts for every supported target.
#
#   scripts/release.sh v0.1.0
#
# writes into dist/:
#
#   devmodels_0.1.0_darwin_arm64.tar.gz    macOS and Linux
#   devmodels_0.1.0_windows_arm64.zip      Windows
#   SHA256SUMS                             checksums for every archive
#
# The release workflow runs this same script, so what a tag publishes is what
# this produces locally. Nothing here tags, pushes, or contacts GitHub.
#
# Builds are CGO_ENABLED=0 and -trimpath, and -buildvcs=false prevents the
# building repository's VCS metadata — commit hash and dirty flag — from being
# embedded in release binaries: the version a release reports is the tag,
# injected through internal/buildinfo.Version, and nothing else.
set -euo pipefail

cd "$(dirname "$0")/.."

usage() {
	cat >&2 <<'USAGE'
usage:
  scripts/release.sh <version>             build and package into dist/
  scripts/release.sh --check-version <ver> validate a version string, build nothing
  scripts/release.sh --notes <version>     print that version's CHANGELOG section
  scripts/release.sh --targets             print the release target matrix

<version> is SemVer with a leading v, for example v0.1.0.
USAGE
}

# The release notes for a version are its CHANGELOG section, so the notes a
# release publishes are the notes in the repository. A tag with no section fails
# rather than publishing an empty release: writing the notes is part of
# preparing a release, not an afterthought.
notes() {
	local v=$1
	check_version "$v"
	local body
	body=$(awk -v want="## $v" '
		$0 == want { grabbing = 1; next }
		grabbing && /^## / { exit }
		grabbing { print }
	' CHANGELOG.md)
	# Trim leading and trailing blank lines.
	body=$(printf '%s\n' "$body" | sed -e '/./,$!d' -e ':a' -e '/^\n*$/{$d;N;ba' -e '}')
	if [ -z "$body" ]; then
		echo "release: CHANGELOG.md has no '## $v' section; add the release notes before tagging" >&2
		return 1
	fi
	printf '%s\n' "$body"
}

# The release target matrix. This list is the single source of truth: the
# workflow builds whatever it says, and the tests assert against it, so a target
# cannot be added in one place and forgotten in another.
targets() {
	cat <<'TARGETS'
darwin/arm64
darwin/amd64
linux/amd64
linux/arm64
windows/amd64
windows/arm64
TARGETS
}

# Version validation follows SemVer (semver.org) with the leading v this project
# tags with. It is split into small checks rather than one expression, because
# the rule that actually bites is narrow and easy to get wrong in a regex: a
# *numeric* identifier may not carry a leading zero, in the version core and in
# prerelease identifiers, but build metadata is free-form and 001 is fine there.

# A numeric identifier: digits only, and no leading zero unless it is just "0".
numeric_identifier() {
	case $1 in
	"" | *[!0-9]*) return 1 ;;
	0) return 0 ;;
	0*) return 1 ;;
	*) return 0 ;;
	esac
}

# An alphanumeric identifier: letters, digits and hyphens, at least one
# character, and at least one character that is not a digit (a digits-only
# identifier is numeric and is checked as such).
alphanumeric_identifier() {
	case $1 in
	"" | *[!0-9A-Za-z-]*) return 1 ;;
	*) return 0 ;;
	esac
}

# Prerelease identifiers are dot-separated; each is numeric (no leading zero) or
# alphanumeric. An empty identifier, as in "alpha..1", is invalid.
check_prerelease() {
	local part IFS=.
	for part in $1; do
		if [ -z "$part" ]; then
			return 1
		fi
		case $part in
		*[!0-9]*)
			alphanumeric_identifier "$part" || return 1
			;;
		*)
			# All digits: the leading-zero rule applies.
			numeric_identifier "$part" || return 1
			;;
		esac
	done
	# A trailing dot leaves an empty final field that the loop cannot see.
	case $1 in
	*.) return 1 ;;
	esac
	return 0
}

# Build metadata identifiers are dot-separated and alphanumeric. Leading zeros
# are explicitly allowed here, so "+001" is valid.
check_build() {
	local part IFS=.
	for part in $1; do
		alphanumeric_identifier "$part" || return 1
	done
	case $1 in
	*.) return 1 ;;
	esac
	return 0
}

check_version() {
	local v=${1-}
	local reject="release: $v is not a SemVer version with a leading v (expected for example v0.1.0)"

	if [ -z "$v" ]; then
		echo "release: no version given; expected something like v0.1.0" >&2
		return 2
	fi
	case $v in
	v*) ;;
	*)
		echo "$reject" >&2
		return 2
		;;
	esac

	local rest=${v#v} pre="" build=""
	# Build metadata is last and starts at the first "+".
	case $rest in
	*+*)
		build=${rest#*+}
		rest=${rest%%+*}
		# A "+" with nothing after it is not build metadata.
		if [ -z "$build" ] || ! check_build "$build"; then
			echo "$reject" >&2
			return 2
		fi
		;;
	esac
	# The prerelease starts at the first "-" of what remains; hyphens are legal
	# inside a prerelease identifier, so only the first one separates.
	case $rest in
	*-*)
		pre=${rest#*-}
		rest=${rest%%-*}
		# A "-" with nothing after it is not a prerelease.
		if [ -z "$pre" ] || ! check_prerelease "$pre"; then
			echo "$reject" >&2
			return 2
		fi
		;;
	esac

	# What is left must be exactly major.minor.patch, each numeric.
	local IFS=.
	# shellcheck disable=SC2086 # deliberate split on IFS
	set -- $rest
	if [ "$#" -ne 3 ]; then
		echo "$reject" >&2
		return 2
	fi
	if ! numeric_identifier "$1" || ! numeric_identifier "$2" || ! numeric_identifier "$3"; then
		echo "$reject" >&2
		return 2
	fi
	return 0
}

# A fixed timestamp for everything that goes into an archive, so two builds of
# the same source produce the same bytes. The commit date is used when there is
# one, which keeps the value meaningful as well as stable; SOURCE_DATE_EPOCH
# overrides it, as the reproducible-builds convention expects.
source_date_epoch() {
	if [ -n "${SOURCE_DATE_EPOCH:-}" ]; then
		echo "$SOURCE_DATE_EPOCH"
		return
	fi
	git log -1 --format=%ct 2>/dev/null || echo 0
}

# touch(1) takes different arguments on BSD and GNU, so format the stamp once
# and use the portable -t form.
touch_stamp() {
	local epoch=$1
	date -u -r "$epoch" +%Y%m%d%H%M.%S 2>/dev/null ||
		date -u -d "@$epoch" +%Y%m%d%H%M.%S
}

# GNU tar and bsdtar spell ownership normalisation differently, and both are in
# play: bsdtar on a macOS dry run, GNU tar on the Linux runner.
make_tar() {
	local out=$1 dir=$2 epoch=$3
	shift 3
	local common=(-C "$dir" --format=ustar)
	if tar --version 2>/dev/null | head -1 | grep -qi 'gnu tar'; then
		common+=(--owner=0 --group=0 --numeric-owner "--mtime=@$epoch")
	else
		common+=(--uid 0 --gid 0 --uname '' --gname '')
	fi
	# gzip -n leaves the original name and timestamp out of the gzip header,
	# which is otherwise the one byte range that differs between two runs.
	tar cf - "${common[@]}" "$@" | gzip -9 -n >"$out"
}

make_zip() {
	local out=$1 dir=$2
	shift 2
	# -X drops the extra file attributes (uid/gid, extended attrs) that would
	# otherwise vary by machine. Entries are added in the order given.
	(cd "$dir" && zip -q -X "$OLDPWD/$out" "$@")
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$@"
	else
		shasum -a 256 "$@"
	fi
}

main() {
	case "${1-}" in
	--targets)
		targets
		return 0
		;;
	--check-version)
		check_version "${2-}"
		return $?
		;;
	--notes)
		notes "${2-}"
		return $?
		;;
	-h | --help)
		usage
		return 0
		;;
	"")
		usage
		return 2
		;;
	esac

	local version=$1
	check_version "$version"
	# Archive names carry the bare version, so v0.1.0 is 0.1.0 in a filename.
	local bare=${version#v}

	local root dist stage
	root=$(pwd)
	dist="$root/dist"
	rm -rf "$dist"
	mkdir -p "$dist"

	local epoch stamp
	epoch=$(source_date_epoch)
	stamp=$(touch_stamp "$epoch")

	echo "building devmodels $version"
	echo "  source date epoch: $epoch"

	local target goos goarch exe name archive
	while read -r target; do
		[ -n "$target" ] || continue
		goos=${target%/*}
		goarch=${target#*/}
		exe=devmodels
		[ "$goos" = windows ] && exe=devmodels.exe

		stage=$(mktemp -d "$dist/.stage.XXXXXX")
		CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
			go build -trimpath -buildvcs=false \
			-ldflags "-X github.com/Tim-Butterfield/devin-model-catalog/internal/buildinfo.Version=$version" \
			-o "$stage/$exe" ./cmd/devmodels

		# Only the executable and the licence. A binary archive is not a copy
		# of the source tree.
		cp LICENSE "$stage/LICENSE"
		TZ=UTC touch -t "$stamp" "$stage/$exe" "$stage/LICENSE"

		name="devmodels_${bare}_${goos}_${goarch}"
		if [ "$goos" = windows ]; then
			archive="dist/$name.zip"
			make_zip "$archive" "$stage" "$exe" LICENSE
		else
			archive="dist/$name.tar.gz"
			make_tar "$root/$archive" "$stage" "$epoch" "$exe" LICENSE
		fi
		rm -rf "$stage"
		echo "  $archive"
	done < <(targets)

	# One checksum file over every archive, sorted by name so the file itself is
	# byte-identical between runs. Paths are relative to dist/, so verification
	# is `cd dist && shasum -a 256 -c SHA256SUMS`.
	(
		cd "$dist"
		# shellcheck disable=SC2012 # names here are generated, never arbitrary
		ls -1 ./*.tar.gz ./*.zip 2>/dev/null | sed 's|^\./||' | LC_ALL=C sort |
			while read -r f; do sha256_of "$f"; done >SHA256SUMS
	)
	echo "  dist/SHA256SUMS"
	echo "done"
}

main "$@"

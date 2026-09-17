# Build helpers. Go is the only requirement; these targets wrap standard go
# commands.
#
# `install` works wherever make and Go do, including a Windows shell. The
# release targets call the shell scripts in scripts/ and so need a POSIX shell
# (on Windows, Git Bash or WSL).
#
# Recipes here avoid shell syntax for that reason: make runs a recipe through
# whatever SHELL it picked, which on Windows is usually cmd.exe, where `[ -n
# "$x" ] || y=z/bin` is not a test but a call to cmd's own `dir` builtin with
# /bin read as a switch.

# git describe fails in a repository with no commits, and make captures only
# stdout, so VERSION is empty there rather than wrong.
VERSION ?= $(shell git describe --tags --always --dirty)
ifeq ($(strip $(VERSION)),)
VERSION := dev
endif

LDFLAGS := -X github.com/Tim-Butterfield/devin-model-catalog/internal/buildinfo.Version=$(VERSION)

# Where `go install` will put it: GOBIN when set, GOPATH/bin otherwise.
GOBIN_DIR := $(shell go env GOBIN)
ifeq ($(strip $(GOBIN_DIR)),)
GOBIN_DIR := $(shell go env GOPATH)/bin
endif

.PHONY: install release release-check

# Install devmodels into $GOBIN, or $(go env GOPATH)/bin when GOBIN is unset.
install:
	go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/devmodels
	@echo installed devmodels $(VERSION) to $(GOBIN_DIR)

# Dry-run the release: build and package every supported target into dist/,
# exactly as the tag-driven workflow does. Publishes nothing. Needs a POSIX
# shell.
#   make release RELEASE_VERSION=v0.1.0
release:
	@[ -n "$(RELEASE_VERSION)" ] || { echo "set RELEASE_VERSION, e.g. make release RELEASE_VERSION=v0.1.0" >&2; exit 2; }
	scripts/release.sh "$(RELEASE_VERSION)"

# The release script's own regression tests; no network, no real cross-compile.
# Needs a POSIX shell.
release-check:
	scripts/release_test.sh

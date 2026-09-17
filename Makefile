# Build helpers. Go is the only requirement; these targets wrap standard go commands.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/Tim-Butterfield/devin-model-catalog/internal/buildinfo.Version=$(VERSION)

.PHONY: install release release-check

# Install devmodels into $GOBIN, or $(go env GOPATH)/bin when GOBIN is unset.
install:
	go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/devmodels
	@dir=$$(go env GOBIN); [ -n "$$dir" ] || dir=$$(go env GOPATH)/bin; echo "installed devmodels $(VERSION) to $$dir"

# Dry-run the release: build and package every supported target into dist/,
# exactly as the tag-driven workflow does. Publishes nothing.
#   make release RELEASE_VERSION=v0.1.0
release:
	@[ -n "$(RELEASE_VERSION)" ] || { echo "set RELEASE_VERSION, e.g. make release RELEASE_VERSION=v0.1.0" >&2; exit 2; }
	scripts/release.sh "$(RELEASE_VERSION)"

# The release script's own regression tests; no network, no real cross-compile.
release-check:
	scripts/release_test.sh

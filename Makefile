.PHONY: build test test-race test-js vet cross check-pii check-pii-history check-pii-test clean

GO ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -trimpath -ldflags "-s -w -X main.version=$(VERSION)"
TARGETS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64
BIN := mail-export-viewer

build:
	CGO_ENABLED=0 $(GO) build $(LDFLAGS) -o $(BIN) ./cmd/$(BIN)

test:
	$(GO) test ./...

test-race:
	CGO_ENABLED=1 $(GO) test -race ./...

# UI unit tests (node 22, built-in test runner; no npm packages).
test-js:
	@if [ -d internal/web/jstest ]; then cd internal/web/jstest && node --test; else echo "no JS tests yet"; fi

vet:
	$(GO) vet ./...

# Four static binaries (CGO off, version injected) plus SHA256SUMS in dist/.
cross:
	rm -rf dist && mkdir -p dist
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build $(LDFLAGS) -o dist/$(BIN)-$$os-$$arch ./cmd/$(BIN) || exit 1; \
	done
	cd dist && (sha256sum $(BIN)-* 2>/dev/null || shasum -a 256 $(BIN)-*) > SHA256SUMS
	cat dist/SHA256SUMS

check-pii: check-pii-test
	scripts/check-pii.sh

# Same patterns over every commit on every ref (diffs, messages, identities).
check-pii-history: check-pii-test
	scripts/check-pii.sh --history

check-pii-test:
	scripts/check-pii_test.sh

clean:
	rm -rf dist $(BIN)

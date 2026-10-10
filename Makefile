GO ?= go
PYTHON ?= python3
VERSION ?=

.PHONY: help build fmt fmt-check vet test test-full docs-check api-check contract-check recovery-check notices-check workflows-check vuln check check-full release

help:
	@printf '%s\n' 'make build       Build bin/cutmy' 'make check       Format, vet, docs, tests and build (integration tests may skip)' 'make check-full  Require media/extractor/PostgreSQL tools, race tests, workflows and vulnerability scan' 'make release VERSION=vX.Y.Z  Package CLI binaries from a clean tagged checkout'

build:
	$(GO) build -trimpath -o bin/cutmy ./cmd/cutmy

fmt:
	gofmt -w cmd internal pkg

fmt-check:
	@test -z "$$(gofmt -l cmd internal pkg)" || { gofmt -l cmd internal pkg; exit 1; }

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-full:
	./scripts/check-tools.sh
	CUTMY_REQUIRE_CONTRACT=1 CUTMY_CONTRACT_PYTHON=$(PYTHON) $(GO) test -race -count=1 ./...

docs-check:
	$(PYTHON) scripts/check-docs.py

api-check:
	$(PYTHON) scripts/check-contract.py
	$(PYTHON) -m unittest discover -s scripts -p contract_test.py

contract-check: api-check
	CUTMY_REQUIRE_CONTRACT=1 CUTMY_CONTRACT_PYTHON=$(PYTHON) $(GO) test -count=1 ./internal/app -run '^TestHTTPPublicContract$$'

recovery-check:
	$(PYTHON) scripts/recovery-acceptance.py --go "$(GO)"

notices-check:
	$(PYTHON) scripts/update-notices.py --check

workflows-check:
	$(GO) run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -shellcheck= -pyflakes=

vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

check: fmt-check vet docs-check api-check test build

check-full: fmt-check vet docs-check api-check notices-check workflows-check test-full vuln build

release:
	./scripts/release.sh "$(VERSION)"

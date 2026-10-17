GO ?= go
PYTHON ?= python3
VERSION ?=
STORAGE_REPORT_DIR ?=
STORAGE_SIZES ?= 10000,100000
STORAGE_SAMPLES ?= 20
SDK_REPORT_DIR ?=

.PHONY: help build fmt fmt-check vet test test-full docs-check api-check native-results-check sdk-controls-check sdk-check performance-protocol-check release-check hosting-controls-check contract-check recovery-controls-check recovery-check storage-capacity-check notices-check workflows-check vuln check-static check-controls check check-full release

help:
	@printf '%s\n' \
		'make build           Build bin/cutmy' \
		'make check           Fast local checks, available tests and build (integration tests may skip)' \
		'make check-full      Full source acceptance: media/extractor/PostgreSQL tools, race tests, workflows and vulnerability scan' \
		'make check-static    Format, vet, documentation and API/schema checks' \
		'make check-controls  Native/SDK/performance/release runner controls; no full acceptance' \
		'make recovery-controls-check  Recovery harness controls; no actual upgrade or restore' \
		'make release VERSION=vX.Y.Z  Package CLI binaries from a clean tagged checkout'

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

native-results-check:
	$(PYTHON) -m unittest discover -s scripts -p native_results_test.py

sdk-controls-check:
	$(PYTHON) -m unittest discover -s scripts -p sdk_checks_test.py

sdk-check: sdk-controls-check
	@test -n "$(SDK_REPORT_DIR)" || { printf '%s\n' 'Set SDK_REPORT_DIR to a new directory outside this checkout.' >&2; exit 1; }
	$(PYTHON) scripts/check-sdks.py --output "$(SDK_REPORT_DIR)" --http

performance-protocol-check:
	$(PYTHON) -m unittest discover -s scripts -p core_performance_test.py

release-check:
	$(PYTHON) -m unittest discover -s scripts -p 'release*_test.py'
	$(PYTHON) -m unittest discover -s scripts -p consumer_process_native_test.py

hosting-controls-check:
	$(PYTHON) -m unittest discover -s deploy -p self_host_test.py
	$(PYTHON) -m unittest discover -s scripts -p self_hosting_acceptance_test.py
	$(PYTHON) -m unittest discover -s scripts -p source_retention_transition_test.py
	$(PYTHON) -m unittest discover -s scripts -p current_installation_acceptance_test.py
	$(PYTHON) -m unittest discover -s scripts -p source_media_cleanup_transition_test.py
	$(PYTHON) -m unittest discover -s scripts -p metadata_resolution_transition_test.py
	$(PYTHON) -m unittest discover -s scripts -p agent_projects_transition_test.py

contract-check: api-check
	CUTMY_REQUIRE_CONTRACT=1 CUTMY_CONTRACT_PYTHON=$(PYTHON) $(GO) test -count=1 ./internal/app -run '^TestHTTPPublicContract$$'

recovery-controls-check:
	$(PYTHON) -m unittest discover -s scripts -p recovery_test.py

recovery-check: recovery-controls-check
	$(PYTHON) scripts/recovery-acceptance.py --go "$(GO)"

storage-capacity-check:
	@test -n "$(STORAGE_REPORT_DIR)" || { printf '%s\n' 'Set STORAGE_REPORT_DIR to a new evidence directory.' >&2; exit 1; }
	$(PYTHON) scripts/storage-capacity.py --go "$(GO)" --output "$(STORAGE_REPORT_DIR)" --sizes "$(STORAGE_SIZES)" --samples "$(STORAGE_SAMPLES)"

notices-check:
	$(PYTHON) scripts/update-notices.py --check

workflows-check:
	$(GO) run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -shellcheck= -pyflakes=

vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

check-static: fmt-check vet docs-check api-check

check-controls: native-results-check sdk-controls-check performance-protocol-check release-check

check: check-static check-controls test build

check-full: check-static check-controls hosting-controls-check notices-check workflows-check test-full vuln build

release:
	./scripts/release.sh "$(VERSION)"

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

export GOTOOLCHAIN := local
COVERAGE_MIN ?= 80.0

.PHONY: toolchain-check fmt-check mod-check lint test race security smoke release-check verify

toolchain-check:
	@version=$$(go version | awk '{print $$3}'); \
	if [ "$$version" != go1.27.0 ]; then \
		printf 'required go1.27.0, got %s\n' "$$version" >&2; \
		exit 1; \
	fi; \
	printf '%s\n' "$$version"

fmt-check:
	@paths=$$(git ls-files -z --cached --others --exclude-standard -- '*.go' | xargs -0 -n 1 gofmt -l); \
	if [ -n "$$paths" ]; then \
		printf 'gofmt required:\n%s\n' "$$paths" >&2; \
		exit 1; \
	fi

mod-check:
	go mod tidy -diff
	go mod verify

lint:
	go vet ./...
	go tool staticcheck ./...
	go tool actionlint

test:
	@mkdir -p .cache
	go test -count=1 -shuffle=on -covermode=atomic -coverprofile=.cache/coverage.out ./...
	@coverage=$$(go tool cover -func=.cache/coverage.out | awk '/^total:/ {gsub("%", "", $$3); print $$3}'); \
	printf 'total coverage: %s%% (minimum %s%%)\n' "$$coverage" "$(COVERAGE_MIN)"; \
	awk -v coverage="$$coverage" -v minimum="$(COVERAGE_MIN)" 'BEGIN { exit (coverage + 0 >= minimum + 0) ? 0 : 1 }'

race:
	go test -race -count=1 -shuffle=on ./...

security:
	go tool govulncheck ./...
	go tool gitleaks dir --config .gitleaks-dir.toml --redact --no-banner .
	go tool gitleaks git --config .gitleaks.toml --redact --no-banner --log-opts='--full-history HEAD --diff-filter=tuxdb' .

smoke:
	@mkdir -p .cache
	@CGO_ENABLED=0 go build -trimpath -buildvcs=true -o .cache/delegatd ./cmd/delegatd
	@set +e; .cache/delegatd doctor --help >.cache/doctor-help.out 2>.cache/doctor-help.err; status=$$?; set -e; \
	if [ "$$status" -ne 0 ]; then \
		printf 'doctor --help exited %s\n' "$$status" >&2; \
		exit 1; \
	fi; \
	if [ -s .cache/doctor-help.err ]; then \
		printf 'doctor --help wrote to stderr\n' >&2; \
		cat .cache/doctor-help.err >&2; \
		exit 1; \
	fi; \
	expected='usage: delegatd doctor --config FILE [--timeout DURATION]'; \
	actual=$$(cat .cache/doctor-help.out); \
	if [ "$$actual" != "$$expected" ]; then \
		printf 'unexpected doctor --help output: %s\n' "$$actual" >&2; \
		exit 1; \
	fi

release-check: export VERSION := $(value VERSION)
release-check: export SOURCE_DATE_EPOCH := $(value SOURCE_DATE_EPOCH)
release-check:
	@set -eu; \
	version="$${VERSION-}"; \
	[ -n "$$version" ] || { printf 'release-check: VERSION is required\n' >&2; exit 1; }; \
	if ! printf '%s\n' "$$version" | grep -E -q '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$$'; then printf 'release-check: VERSION must be strict semver core\n' >&2; exit 1; fi; \
	if [ -n "$${SOURCE_DATE_EPOCH-}" ]; then epoch="$${SOURCE_DATE_EPOCH}"; else epoch=$$(git show -s --format=%ct HEAD); fi; \
	case "$${epoch}" in ''|*[!0-9]*) printf 'release-check: SOURCE_DATE_EPOCH must be decimal\n' >&2; exit 1;; esac; \
	go test -tags release_integration ./internal/releasetool -run '^TestPackageRelease' -count=1; \
	first='.cache/release-check-first'; second='.cache/release-check-second'; \
	rm -rf "$$first" "$$second"; mkdir -p "$$first" "$$second"; \
	expected=''; \
	for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do \
		goos=$${target%/*}; goarch=$${target#*/}; name="delegatd_$${version}_$${goos}_$${goarch}"; \
		if [ "$$goos" = windows ]; then archive="$$name.zip"; else archive="$$name.tar.gz"; fi; \
		expected="$$expected $$archive $$name.spdx.json"; \
		VERSION="$$version" GOOS="$$goos" GOARCH="$$goarch" SOURCE_DATE_EPOCH="$$epoch" OUT_DIR="$$first" scripts/package-release.sh >/dev/null; \
		VERSION="$$version" GOOS="$$goos" GOARCH="$$goarch" SOURCE_DATE_EPOCH="$$epoch" OUT_DIR="$$second" scripts/package-release.sh >/dev/null; \
	done; \
	check_tree() { \
		directory=$$1; count=0; \
		for path in "$$directory"/*; do \
			[ -e "$$path" ] || [ -L "$$path" ] || continue; \
			name=$${path##*/}; \
			case " $$expected " in *" $$name "*) ;; *) printf 'release-check: unexpected file %s\n' "$$path" >&2; exit 1;; esac; \
			count=$$((count + 1)); \
		done; \
		for name in $$expected; do [ -f "$$directory/$$name" ] || { printf 'release-check: missing file %s/%s\n' "$$directory" "$$name" >&2; exit 1; }; done; \
		[ "$$count" -eq 12 ] || { printf 'release-check: expected 12 files in %s, got %s\n' "$$directory" "$$count" >&2; exit 1; }; \
	}; \
	check_tree "$$first"; check_tree "$$second"; \
	for name in $$expected; do cmp "$$first/$$name" "$$second/$$name" >/dev/null || { printf 'release-check: nondeterministic file %s\n' "$$name" >&2; exit 1; }; done; \
	if [ -L dist ]; then printf 'release-check: dist must not be a symlink\n' >&2; exit 1; fi; \
	rm -rf dist; mkdir -p dist; \
	for name in $$expected; do cp "$$first/$$name" "dist/$$name"; done; \
	host_goos=$$(go env GOHOSTOS); host_goarch=$$(go env GOHOSTARCH); \
	GOOS="$$host_goos" GOARCH="$$host_goarch" go run ./internal/releasetool checksums --dir dist --output dist/SHA256SUMS; \
	dist_count=0; for path in dist/*; do [ -e "$$path" ] || [ -L "$$path" ] || continue; name=$${path##*/}; case " $$expected SHA256SUMS " in *" $$name "*) ;; *) printf 'release-check: unexpected dist file %s\n' "$$path" >&2; exit 1;; esac; dist_count=$$((dist_count + 1)); done; \
	[ "$$dist_count" -eq 13 ] || { printf 'release-check: expected 13 dist files, got %s\n' "$$dist_count" >&2; exit 1; }; \
	[ "$$(wc -l < dist/SHA256SUMS | tr -d ' ')" -eq 12 ] || { printf 'release-check: SHA256SUMS must contain 12 entries\n' >&2; exit 1; }; \
	printf 'release-check: deterministic six-target package set verified\n'

verify:
	$(MAKE) --no-print-directory toolchain-check
	$(MAKE) --no-print-directory fmt-check
	$(MAKE) --no-print-directory mod-check
	$(MAKE) --no-print-directory lint
	.github/scripts/check-pr-policy.sh --self-test
	scripts/verify-release-tag.sh --self-test
	$(MAKE) --no-print-directory test
	$(MAKE) --no-print-directory race
	$(MAKE) --no-print-directory security
	@paths=$$(git ls-files -z --cached --others --exclude-standard -- '*.sh' | xargs -0 -n 1 bash -n); \
	if [ -n "$$paths" ]; then \
		printf 'shell syntax failed:\n%s\n' "$$paths" >&2; \
		exit 1; \
	fi
	$(MAKE) --no-print-directory smoke

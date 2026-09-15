.PHONY: build install check runner cleanup

build:
	go build -trimpath -buildvcs=false -o bin/devbox ./cmd/devbox

runner:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o bin/devbox-runner-linux-amd64 ./cmd/devbox-runner

cleanup:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -tags lambda.norpc -o bin/cleanup/bootstrap ./cmd/devbox-cleanup
	python3 scripts/package-cleanup.py bin/cleanup/bootstrap bin/devbox-cleanup-linux-amd64.zip

install:
	go install -trimpath -buildvcs=false ./cmd/devbox

check:
	@test -z "$$(gofmt -l cmd internal profiles)" || (gofmt -l cmd internal profiles; exit 1)
	go mod verify
	go build ./...
	go vet ./...
	go test ./...

TOFU ?= tofu
.PHONY: infra-check
infra-check: runner cleanup
	$(TOFU) fmt -check -recursive infra
	$(TOFU) -chdir=infra/state-bootstrap init -backend=false -input=false -lockfile=readonly
	$(TOFU) -chdir=infra/state-bootstrap validate
	$(TOFU) -chdir=infra/state-bootstrap test
	$(TOFU) -chdir=infra/foundation init -backend=false -input=false -lockfile=readonly
	$(TOFU) -chdir=infra/foundation validate
	@tofu_test_output=$$(mktemp); trap 'rm -f "$$tofu_test_output"' EXIT; \
	  tofu_test_status=0; $(TOFU) -chdir=infra/foundation test -json -verbose > "$$tofu_test_output" || tofu_test_status=$$?; \
	  DEVBOX_TEST_TOFU_OUTPUT="$$tofu_test_output" go test ./internal/foundation -run TestOpenTofuExport -v || exit 1; \
	  test "$$tofu_test_status" -eq 0

.PHONY: build install check

build:
	go build -trimpath -buildvcs=false -o bin/devbox ./cmd/devbox

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
infra-check:
	$(TOFU) fmt -check -recursive infra
	$(TOFU) -chdir=infra/state-bootstrap init -backend=false -input=false -lockfile=readonly
	$(TOFU) -chdir=infra/state-bootstrap validate
	$(TOFU) -chdir=infra/state-bootstrap test
	$(TOFU) -chdir=infra/foundation init -backend=false -input=false -lockfile=readonly
	$(TOFU) -chdir=infra/foundation validate
	$(TOFU) -chdir=infra/foundation test

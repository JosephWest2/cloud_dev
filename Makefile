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

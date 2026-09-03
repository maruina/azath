VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
LDFLAGS  = -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)"

.PHONY: build test lint lint-install vet clean

build:
	mkdir -p bin
	go build $(LDFLAGS) -o bin/azath ./cmd/azath

test:
	go test ./... -v -race -count=1

lint: lint-install
	go vet ./...
	golangci-lint run ./...

lint-install:
	@which golangci-lint > /dev/null 2>&1 || \
		(echo "golangci-lint not found. Install with: brew install golangci-lint" && exit 1)

vet:
	go vet ./...

clean:
	rm -rf bin/

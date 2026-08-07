VERSION ?= dev
LDFLAGS = -ldflags "-X github.com/spotify/confidence-metrics-sync/cmd.version=$(VERSION)"

.PHONY: build test vet fmt-check ci snapshot clean

build:
	go build $(LDFLAGS) -o bin/confidence-metrics .

test:
	go test ./...

vet:
	go vet ./...

fmt-check:
	@files="$$(gofmt -l .)"; if [ -n "$$files" ]; then echo "The following files need gofmt:"; echo "$$files"; exit 1; fi

ci: fmt-check vet test build

snapshot:
	goreleaser build --snapshot --clean

clean:
	rm -rf bin dist

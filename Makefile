.PHONY: build test vet fmt lint

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o releasejo ./cmd/releasejo

test:
	go test ./...

vet:
	go vet ./...

# fmt check (CI-style): fails if anything is unformatted
lint: vet
	@test -z "$$(gofmt -l .)" || { echo "gofmt needs:"; gofmt -l .; exit 1; }

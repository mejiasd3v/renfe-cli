.PHONY: build test check

build:
	go build -trimpath -ldflags="-s -w" -o renfe ./cmd/renfe

test:
	go test ./...

check: test
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

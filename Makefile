.PHONY: build test check dist

VERSION := $(shell sed -n 's/^VERSION="\(.*\)"$$/\1/p' skills/renfe/scripts/renfe)

build:
	go build -trimpath -ldflags="-s -w" -o renfe ./cmd/renfe

test:
	go test ./...

check: test
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...
	shellcheck skills/renfe/scripts/renfe scripts/dist.sh

# Release archives for VERSION (from the skill launcher) into dist/.
dist:
	./scripts/dist.sh $(VERSION)

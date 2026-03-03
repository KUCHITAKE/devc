VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-X main.version=$(VERSION)" -o devc .

lint:
	golangci-lint run ./...

test:
	go test ./...

install: build
	install -Dm755 devc $(HOME)/.local/bin/devc

setup-hooks:
	pre-commit install

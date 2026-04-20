VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

GO_IMAGE     := golang:1.25
LINT_IMAGE   := golangci/golangci-lint:v2.10.1
DOCKER_RUN   := docker run --rm \
	-v $(CURDIR):/src \
	-v devc-gomod:/go/pkg/mod \
	-v devc-gobuild:/root/.cache/go-build \
	-w /src

build:
	$(DOCKER_RUN) $(GO_IMAGE) \
		go build -buildvcs=false -ldflags "-X main.version=$(VERSION)" -o devc ./cmd/devc

lint:
	$(DOCKER_RUN) $(LINT_IMAGE) \
		golangci-lint run ./...

test:
	$(DOCKER_RUN) $(GO_IMAGE) \
		go test ./...

install: build
	install -Dm755 devc $(HOME)/.local/bin/devc

clean-cache:
	docker volume rm -f devc-gomod devc-gobuild

.PHONY: build lint test install clean-cache

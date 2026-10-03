GO ?= go
STATICCHECK ?= $(GO) run honnef.co/go/tools/cmd/staticcheck@latest
GOVULNCHECK ?= $(GO) run golang.org/x/vuln/cmd/govulncheck@latest

.PHONY: build test vet fmt tidy tidy-check lint vuln check

build:
	$(GO) build ./...

# The race detector is the point: the backend hands events between the
# agent's goroutine and the client's.
test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

# Fails when go mod tidy would change go.mod or go.sum, without writing.
tidy-check:
	$(GO) mod tidy -diff

fmt:
	gofmt -l . && test -z "$$(gofmt -l .)"

lint:
	$(STATICCHECK) ./...

vuln:
	$(GOVULNCHECK) ./...

# Everything CI runs. While go.mod requires an agentsession older than
# the one that ships Follow, run it with GOWORK set to a workspace file
# that uses the agentsession tree: without it the module does not
# compile.
check: fmt tidy-check vet lint vuln test

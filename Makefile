.PHONY: test build run clean tidy

GOFLAGS ?= -mod=mod

test:
	go test $(GOFLAGS) ./internal/...

build:
	mkdir -p bin
	go build $(GOFLAGS) -o bin/ctfagent-daemon ./cmd/ctfagent-daemon

run: build
	./bin/ctfagent-daemon

clean:
	rm -rf bin/

# tidy is a no-op helper when the module proxy is unavailable;
# dependencies live under vendor/ and are wired via go.mod replace.
tidy:
	@echo "dependencies are vendored under ./vendor (see go.mod replace directives)"

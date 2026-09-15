.PHONY: build test check release

build:
	mkdir -p bin
	go build -buildvcs=false -trimpath -o bin/webterm ./cmd/webterm

test:
	go test ./...

# check must not modify the tree: it reports unformatted files and fails instead
# of rewriting them, so CI cannot silently "fix" a commit. The Go toolchain
# already honours GOCACHE and GOMODCACHE from the environment, so they are left
# alone here; forcing them to an empty directory made every run re-download the
# module cache and fail whenever the network was unavailable.
check:
	@unformatted="$$(gofmt -l cmd internal)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt: the following files need formatting (run 'make fmt'):"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	go vet ./...
	go test ./...

fmt:
	gofmt -w cmd internal

release:
	./scripts/release.sh

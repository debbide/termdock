.PHONY: build test check release
build:
	mkdir -p bin
	go build -buildvcs=false -trimpath -o bin/webterm ./cmd/webterm
test:
	go test ./...
check:
	GOCACHE=$${GOCACHE:-/tmp/webterm-go-build} GOMODCACHE=$${GOMODCACHE:-/tmp/webterm-go-mod} gofmt -w cmd internal
	GOCACHE=$${GOCACHE:-/tmp/webterm-go-build} GOMODCACHE=$${GOMODCACHE:-/tmp/webterm-go-mod} go vet ./...
	GOCACHE=$${GOCACHE:-/tmp/webterm-go-build} GOMODCACHE=$${GOMODCACHE:-/tmp/webterm-go-mod} go test ./...
release:
	./scripts/release.sh

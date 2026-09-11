#!/bin/sh
set -eu

VERSION=${VERSION:-dev}
OUTPUT_DIR=${OUTPUT_DIR:-dist}
mkdir -p "$OUTPUT_DIR"

for ARCH in amd64 arm64; do
  TARGET="$OUTPUT_DIR/webterm-linux-$ARCH"
  CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -buildvcs=false -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$TARGET" ./cmd/webterm
done

(cd "$OUTPUT_DIR" && sha256sum webterm-linux-* > SHA256SUMS)
echo "Release artifacts written to $OUTPUT_DIR"

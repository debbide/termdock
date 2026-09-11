# Quick Start

Build and run locally without a tunnel:

```sh
make build
./bin/webterm --config configs/config.example.json
```

To use a Cloudflare Quick Tunnel, set `cloudflare.mode` to `quick` and ensure the configured `cloudflared` binary exists. The process prints the public URL and one-time token to standard output.

For a published release, the installer can download the matching amd64 or arm64 binary and install `cloudflared` automatically:

```sh
curl -fsSL https://example.com/install.sh | sudo RELEASE_BASE_URL=https://example.com sh
```

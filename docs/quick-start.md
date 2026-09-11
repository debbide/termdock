# Quick Start

Build and run locally without a tunnel:

```sh
make build
./bin/webterm --config configs/config.example.json
```

To use a Cloudflare Quick Tunnel, set `cloudflare.mode` to `quick` and ensure the configured `cloudflared` binary exists. The process prints the public URL and one-time token to standard output.

The repository-hosted installer downloads the matching amd64 or arm64 binary from a published release and installs `cloudflared` automatically:

```sh
curl -fsSL https://raw.githubusercontent.com/debbide/termdock/main/scripts/install.sh | sudo sh
```

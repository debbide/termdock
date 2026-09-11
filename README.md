# TermDock

TermDock is a secure, self-contained web terminal for Linux. It serves an embedded browser client, creates local PTY sessions without SSH, supports one-time-token authentication, and can manage Cloudflare Quick or fixed tunnels.

## Build

```sh
make check
make build
```

Run locally with:

```sh
./bin/webterm --config configs/config.example.json
```

See `docs/quick-start.md`, `docs/fixed-tunnel.md`, and `docs/security.md` for deployment details. Installation assets are provided in `scripts/` and `packaging/systemd/`.

For release-based installation on Debian or Ubuntu, publish the files produced by `make release`, then run:

```sh
curl -fsSL https://example.com/install.sh | sudo RELEASE_BASE_URL=https://example.com sh
```

The installer detects amd64 or arm64, verifies the TermDock SHA-256 checksum, installs `cloudflared` when missing, creates the service account, and starts the systemd service.

Pushing a `v*` tag runs the GitHub Actions release workflow, executes vet and tests, builds both Linux architectures, and publishes the binaries, checksum file, and installer as GitHub Release assets.

TermDock is a browser terminal backed directly by a local PTY. It does not require SSH or `sshd`.

## Development

```bash
go run ./cmd/webterm
```

The server listens on `127.0.0.1:7681` and prints a one-time token. For direct local HTTP development, set `security.cookie_secure` to `false` in a permission-restricted JSON config file and pass `--config`.

## Security boundaries

- Anonymous terminal and status access is denied.
- Authentication uses a single-use random token and signed, short-lived HttpOnly cookie.
- WebSocket requests require a valid cookie and matching or explicitly trusted Origin.
- PTY processes run with the service account's privileges and their process group is terminated on disconnect.
- The default listener is loopback-only. Put HTTPS or Cloudflare Tunnel in front of it before remote use.
- Never run the service as root unless root terminal access is explicitly intended.

The implementation includes secure PTY sessions, authentication, tunnel lifecycle management, systemd packaging, and release artifacts. Clean-system platform validation and hosted release publication remain release-environment tasks.

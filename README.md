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

## One-command installation

TermDock publishes static Linux binaries for AMD64 (`x86_64`) and ARM64 (`aarch64`). On Debian or Ubuntu, install the latest GitHub Release with:

```sh
curl -fsSL https://github.com/debbide/termdock/releases/latest/download/install.sh | sudo sh
```

The installer detects the CPU architecture, verifies the SHA-256 checksum, creates the `webterm` service account, and starts TermDock. It uses systemd when a working systemd instance is available, otherwise it automatically falls back to a background process with logs in `/var/log/webterm/webterm.log`. Without parameters, TermDock listens on `127.0.0.1:7681` and prints a random one-time token to the service log.

Only two optional environment variables are supported:

- `WEBTERM_TOKEN`: fixed reusable login token. If omitted, a random one-time token is written to the service log.
- `WEBTERM_PORT`: listening port, default `7681`.

Install with a fixed token:

```sh
curl -fsSL https://github.com/debbide/termdock/releases/latest/download/install.sh | \
  sudo WEBTERM_TOKEN='change-this-token' sh
```

Set both token and port:

```sh
curl -fsSL https://github.com/debbide/termdock/releases/latest/download/install.sh | \
  sudo WEBTERM_TOKEN='change-this-token' WEBTERM_PORT=8080 sh
```

View the random token when `WEBTERM_TOKEN` was omitted:

```sh
sudo journalctl -u webterm -n 30 --no-pager
```

On systems without systemd, view the token and log with:

```sh
sudo tail -n 30 /var/log/webterm/webterm.log
```

After installation, edit `/etc/webterm/config.json` as needed and use:

```sh
sudo systemctl status webterm
sudo systemctl restart webterm
sudo journalctl -u webterm -f
```

## GitHub Releases

Pushing a `v*` tag runs the GitHub Actions workflow, executes `go vet` and all tests, builds Linux AMD64 and ARM64 binaries, generates `SHA256SUMS`, and publishes the binaries plus the one-command installer as GitHub Release assets:

```sh
git tag v1.0.0
git push origin v1.0.0
```

You can also open **Actions → Release → Run workflow**, enter a tag such as `v1.0.0`, and start the release manually. The workflow creates or updates the GitHub Release for the entered tag.

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

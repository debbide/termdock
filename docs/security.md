# Security

TermDock listens on `127.0.0.1` by default, requires one-time-token authentication, validates WebSocket origins, limits concurrent sessions, and terminates PTY process groups when sessions close.

## Authentication

- The one-time token is stored only as a SHA-256 digest and is consumed on first use.
- A fixed token supplied through `WEBTERM_ACCESS_TOKEN` stays reusable; the process reports only that the token came from the environment so a long-lived secret never reaches the journal.
- Session cookies carry a random identifier plus a signed expiry, so the server can revoke an individual cookie. Logging out revokes the cookie and ends the terminal session; a stolen cookie stops working immediately.
- There is deliberately no per-address login rate limit. Behind a tunnel every request arrives from `127.0.0.1`, so counting failures by peer address would let any remote caller lock the real operator out for the whole window. Tokens are 256-bit random values, which makes guessing infeasible; add Cloudflare Access in front of the tunnel if you want an additional authentication layer.
- Omitting `security.cookie_secure` keeps the secure default. Only an explicit `false` disables it, which is intended for local HTTP development.

## Proxy headers

`X-Forwarded-Host` and `X-Forwarded-Proto` are honored only when the direct peer is loopback or listed in `security.trusted_proxies` (IP or CIDR). A remote client therefore cannot name an arbitrary host to satisfy origin validation or force a secure cookie. The default deployment — cloudflared forwarding to `127.0.0.1` — needs no extra configuration; add entries only when a proxy connects over a non-loopback address.

## File manager confinement

The file API is restricted to `terminal.working_directory`. Absolute paths and `..` sequences that leave that directory are rejected, and the working directory itself cannot be deleted, renamed, or moved. The PTY is not confined, so an authenticated user still has full shell access; the confinement exists to keep the HTTP surface from being broader than the terminal.

Archive extraction rejects absolute paths, `..` traversal, symbolic links, and hard links, and enforces entry-count and 1 GB extracted-size limits.

## Deployment

The packaged service runs as the dedicated `webterm` account with systemd sandboxing. Do not grant this account unrestricted sudo access. Keep configuration group-writable and world-writable permissions disabled, and keep fixed-tunnel token files at mode `0600` or stricter.

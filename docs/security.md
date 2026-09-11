# Security

WebTerm CF listens on `127.0.0.1` by default, requires one-time-token authentication, validates WebSocket origins, limits concurrent sessions, and terminates PTY process groups when sessions close.

The packaged service runs as the dedicated `webterm` account with systemd sandboxing. Do not grant this account unrestricted sudo access. Keep configuration group-writable and world-writable permissions disabled, and keep fixed-tunnel token files at mode `0600` or stricter.

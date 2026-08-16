# Security

## Reporting a vulnerability

Please report security issues through **[GitHub private vulnerability reporting](https://github.com/stalexteam/restream_go/security/advisories/new)** — the *Report a vulnerability* button on the repository's Security tab. It opens a private thread visible only to you and the maintainer, so the problem does not become public before there is a fix.

Do not use a normal issue for this: issues are world-readable the moment you press submit, which hands the exploit to everyone still running the vulnerable version. If private reporting is unavailable to you for some reason, open an issue that says only that you have a security report and asks for a contact channel — no details, no reproduction — and it will be moved somewhere private.

Include what you did, what happened, and, if you have one, a minimal reproduction.

This is a small project maintained by one person in their spare time: expect a best-effort reply, not a guaranteed response window, and there is no bounty programme. Only the `main` branch is supported; there are no tagged releases yet, so "the current commit" is the only version that gets fixes.

## What this software assumes

restream-controller is built for a single-tenant VPS that you own and operate, reachable only by you. It is **not** hardened for exposure to the open internet, and several of its interfaces are unencrypted by design. Read this before opening a port.

**The dashboard and its WebSocket run over plain HTTP.** Authentication is a bearer token embedded in the URL, checked with a constant-time comparison — but the token itself, and everything the dashboard shows or sets (platform stream keys included), travel in clear text. Do not expose port 8790 to the internet. Restrict it to your own IP with a firewall, or put a TLS-terminating reverse proxy in front of it.

**Ingest credentials are also unencrypted.** RTMP has no transport security, so the publish password sits in the query string of the URL you paste into OBS. The SRT ingest carries the same credentials inside its `streamid`. Both are only as private as the network path between you and your VPS.

**Ports.** Only three need to be reachable, and ideally only from your own address: `1935/tcp` (RTMP ingest), `8790/tcp` (dashboard and WebSocket), and `8890/udp` (SRT ingest, only if you use SRT sources). Everything else MediaMTX can expose — RTSP, HLS, WebRTC, MoQ and its control API — is switched off in the rendered configuration. That configuration is generated into `tmp/mediamtx.yml` before every MediaMTX start, from a template compiled into the controller binary, so there is no separate file to keep in sync — and no way to leave one of those services on by accident.

**Secrets on disk.** `config.json`, next to the binary, is the single source of truth for the two RTMP passwords, the dashboard token and every platform stream key. `install.sh` restricts it to its owner (`chmod 600`, along with the generated `obs-dock.html` and `obs-source.html`, which embed the dashboard token), and locks `tmp/` and `logs/` down to `chmod 700`. Logs are restricted because they can contain push URLs with platform stream keys in them; keep that in mind before pasting log excerpts into an issue.

**Privileges.** The controller and MediaMTX run as the user who starts them, not as root — the systemd unit `install.sh` writes runs as the user who ran the installer, and the controller starts MediaMTX as a child of itself. `install.sh` uses `sudo` only to install the system packages and to write the unit file; MediaMTX is downloaded into the installation directory unprivileged. Do not run the controller as root.

**Outbound traffic.** The controller connects out to the platforms you configure, and — only if you enable Enhanced Broadcasting on a Twitch platform — to Twitch's go-live endpoint to obtain the ingest URL for that broadcast. There is no telemetry, no analytics and no auto-update.

## Known limitations that are not vulnerabilities

No HTTPS on the controller's own HTTP server, no state recovery if the controller restarts mid-broadcast, and no multi-user or role model — the dashboard token is all-or-nothing access. These are documented trade-offs for a single-operator tool, not oversights. If your deployment needs any of them, put the service behind infrastructure that provides them.

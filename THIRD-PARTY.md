# Third-party components

This repository contains no vendored or bundled third-party code. The controller is Go using only the standard library — `go.mod` declares no requirements, so nothing is fetched from the module proxy, and its WebSocket server, FLV/MPEG-TS parsers, RTMP push client and process-stats reader are all written here. The dashboard it serves uses no third-party JavaScript or CSS, and the application icon compiled into the Windows executable is the project's own.

The project does depend on external programs at runtime. `install.sh` obtains them on your machine, from your distribution's package manager or from the upstream project's own release page — none of them are redistributed as part of this repository, so what you get is governed by the terms each of them ships under.

| Component | Used for | Obtained by | License |
| --- | --- | --- | --- |
| [MediaMTX](https://github.com/bluenviron/mediamtx) v1.19.3 | RTMP and SRT ingest, plus readback of published streams | `install.sh` downloads the official release tarball from GitHub | MIT |
| [FFmpeg](https://ffmpeg.org/) | relay reads, backup-clip normalisation, every outbound push, and the SRT byte transport | `apt-get install ffmpeg`, or `winget install Gyan.FFmpeg` on Windows | Core is LGPL-2.1-or-later; the builds Debian and Ubuntu ship enable GPL components (such as libx264), which makes those binaries GPL-2.0-or-later |
| [Go](https://go.dev/) standard library | building the controller; its runtime and stdlib are linked into the binary | your own toolchain at build time — see [Build from sources](Doc/Build/Build.md) | BSD-3-Clause |
| curl, ca-certificates | fetching the MediaMTX release; TLS root store | `apt-get install curl ca-certificates` | curl license (MIT-style); Mozilla CA bundle is MPL-2.0 |

FFmpeg is invoked as a separate process — the controller never links against it — so its terms apply to the binary your system installs, not to this source tree. It carries the SRT transport too: the controller drives `ffmpeg -f data` for that and no longer calls `srt-live-transmit`, so libsrt reaches the project only as whatever FFmpeg was built against.

A prebuilt `restreamd` — a release binary, or one you built yourself and handed to someone — is a different matter, because Go links its runtime and standard library into the executable. Ship the Go BSD-3-Clause notice with it. That is the only third-party code inside the binary: MediaMTX and FFmpeg stay outside it, fetched on the target machine by `install.sh` or `install.ps1`.

If you ever go further and distribute a bundle that carries those too (a Docker image, a tarball with `bin/mediamtx` inside, a VM appliance), this changes again: at that point you are redistributing FFmpeg and MediaMTX, and their notice and source-availability requirements — GPL for a typical FFmpeg build, MIT for MediaMTX, plus MPL-2.0 for libsrt if the FFmpeg you ship was built with it — attach to what you ship. Regenerate this list at that point rather than trusting it to still be accurate.

[obs-multi-rtmp](https://github.com/sorayuki/obs-multi-rtmp) is mentioned in the documentation as an optional OBS plugin for feeding extra sources. It is not required, not installed by this project, and not distributed here.

# Third-party components

This repository contains no vendored or bundled third-party code. The controller is Go using only the standard library — `go.mod` declares no requirements, so nothing is fetched from the module proxy, and its WebSocket server, FLV/MPEG-TS parsers, RTMP push client and process-stats reader are all written here. The dashboard it serves uses no third-party JavaScript or CSS.

The project does depend on external programs at runtime. `install.sh` obtains them on your machine, from your distribution's package manager or from the upstream project's own release page — none of them are redistributed as part of this repository, so what you get is governed by the terms each of them ships under.

| Component | Used for | Obtained by | License |
| --- | --- | --- | --- |
| [MediaMTX](https://github.com/bluenviron/mediamtx) v1.19.3 | RTMP and SRT ingest, plus readback of published streams | `install.sh` downloads the official release tarball from GitHub | MIT |
| [FFmpeg](https://ffmpeg.org/) | relay reads, backup-clip normalisation, most outbound pushes | `apt-get install ffmpeg` | Core is LGPL-2.1-or-later; the builds Debian and Ubuntu ship enable GPL components (such as libx264), which makes those binaries GPL-2.0-or-later |
| [srt-tools](https://github.com/Haivision/srt) (`srt-live-transmit`, libsrt) | SRT readback of multitrack sources and SRT output transport | `apt-get install srt-tools` | MPL-2.0 |
| [Go](https://go.dev/) standard library | building the controller; its runtime and stdlib are linked into the binary | your own toolchain at build time — see [Build from sources](Doc/Build/Build.md) | BSD-3-Clause |
| curl, ca-certificates | fetching the MediaMTX release; TLS root store | `apt-get install curl ca-certificates` | curl license (MIT-style); Mozilla CA bundle is MPL-2.0 |

FFmpeg and `srt-live-transmit` are invoked as separate processes — the controller never links against them — so their terms apply to the binaries your system installs, not to this source tree.

A prebuilt `restreamd` — a release binary, or one you built yourself and handed to someone — is a different matter, because Go links its runtime and standard library into the executable. Ship the Go BSD-3-Clause notice with it. That is the only third-party code inside the binary: MediaMTX, FFmpeg and srt-tools stay outside it, fetched on the target machine by `install.sh`.

If you ever go further and distribute a bundle that carries those too (a Docker image, a tarball with `bin/mediamtx` inside, a VM appliance), this changes again: at that point you are redistributing FFmpeg, libsrt and MediaMTX, and their notice and source-availability requirements — GPL for a typical FFmpeg build, MPL-2.0 for libsrt, MIT for MediaMTX — attach to what you ship. Regenerate this list at that point rather than trusting it to still be accurate.

[obs-multi-rtmp](https://github.com/sorayuki/obs-multi-rtmp) is mentioned in the documentation as an optional OBS plugin for feeding extra sources. It is not required, not installed by this project, and not distributed here.

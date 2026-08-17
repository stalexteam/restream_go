# Build from sources

The controller is a single Go binary with no third-party modules — the dashboard, the OBS files and the MediaMTX config template are all embedded in it. Building is one script; deploying is one directory (see the [README](../../README.md) for the overview, and [Setup](../Setup/Setup.md) for what to do after `install.sh`).

## Prerequisites

- **Go 1.26 or newer** on the machine you build on (`go.mod` pins `go 1.26`). That's the whole list: nothing is fetched from the module proxy, and no C toolchain is involved.
- Nothing on the VPS: `ffmpeg` and MediaMTX are installed there by `install.sh`, not built here.
- `tar` on the build machine, for packing the release archives. Present on any Linux, and on Windows since 10 1803.

## Build

```bash
git clone https://github.com/stalexteam/restream_go.git restream_go
cd restream_go
./build.sh
```

The Windows executable carries an application icon. `build.sh` generates it on Windows targets — the icon lives in `internal/assets/restreamd.ico`, and the linker reads it through `resource_windows_amd64.syso`, which is a build artifact, not a checked-in file. Building without `build.sh` still works; the executable just has no icon unless you run the generator first:

```bash
go run internal/assets/mkicon.go
```

That generator is plain Go with no external toolchain: `cvtres` output is unusable here because it emits absolute symbols (`@comp.id`, `@feat.00`) that make the Go linker fail with `sectnum < 0!`.

One run builds **both** platforms and packs the release into `build/`:

```
build/
  restreamd                       Linux binary — dashboard, OBS templates and mediamtx.yml template inside
  restreamd.exe                   the same for Windows, with the application icon
  install.sh / install.ps1        the installers, unpacked from internal/scripts/*.template
  build_<version>_linux64.tar.gz  release asset: restreamd + install.sh
  build_<version>_win64.tar.gz    release asset: restreamd.exe + install.ps1
```

The two archives are what you upload to a GitHub release. Both are `.tar.gz` rather than a zip for Windows, because `tar` is the one archiver present on both systems out of the box — `C:\Windows\System32\tar.exe` unpacks the Windows asset with no extra software.

Use a different toolchain with the `GO` variable — handy when `go` isn't on `PATH`:

```bash
GO=/usr/local/go/bin/go ./build.sh
```

## Versioning

`internal/scripts/VERSION` holds the version of the build you are about to make, as `MAJOR.MINOR.PATCH`. `build.sh` reads it, stamps both archive names with it, then increments the patch for next time — so consecutive runs never collide and never need a flag.

Raise `MAJOR` or `MINOR` by editing that file directly; the number you write is the one the next build carries, nothing is skipped. To rebuild without consuming a version — normal while iterating — set `BUMP=0`:

```bash
BUMP=0 ./build.sh
```

The version reaches the archive names only; the binary itself does not report it yet.

## Targets

`build.sh` cross-compiles `linux/amd64` and `windows/amd64` in one run — the whole point being that both halves of a release carry the same version. The controller is portable Go with no C toolchain involved, so this works from either platform.

Building for something else (an ARM VPS, say) means editing the `GOOS`/`GOARCH` pair in `build.sh`, and matching it in `install.sh`, which downloads the `linux_amd64` MediaMTX build. `CGO_ENABLED` is not set anywhere: with no cgo in the tree the binary is static regardless, so the server's glibc version does not matter.

`install.sh` is Debian/Ubuntu-only; Windows has `install.ps1` instead, which pulls ffmpeg through winget and MediaMTX from GitHub.

## Deploy

```bash
scp build/build_0.1.0_linux64.tar.gz user@vps:~/
ssh user@vps 'mkdir -p restream && tar -xzf build_0.1.0_linux64.tar.gz -C restream && cd restream && ./install.sh'
```

The directory you unpack into becomes the **installation root**: `install.sh` creates `bin/`, `media/`, `logs/` and `tmp/` next to the binary, and `restreamd --config` writes `config.json`, `obs-dock.html` and `obs-source.html` there. Upgrading is unpacking a newer archive over the same directory — that replaces only `restreamd` and `install.sh`, leaving your config, media and logs alone.

`install.sh` installs the packages and MediaMTX, registers the `restreamd` systemd unit, then hands over to `restreamd --config`, which prints the dashboard link and the OBS file paths. Continue from [Setup](../Setup/Setup.md).

## Tests

```bash
go test ./...
```

The suite is self-contained: no network, no external fixtures, no other runtime — everything a test needs is built by the test itself. It exercises the RTMP/FLV/TS wire code, the routing and timeline logic and the HTTP/WS API, so it's a meaningful check after a change, not a smoke test.

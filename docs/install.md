# Installing Pelt

[Back to README](../README.md)

## One-line installer, Linux and macOS

Works on amd64 and arm64:

```sh
curl -fsSL https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.sh | sh
```

It picks the release archive for your OS and CPU, checks its SHA-256 against
the release's `checksums.txt`, and installs `pelt` to `/usr/local/bin` (using
`sudo` only if that directory is not writable). Options:

```sh
# a specific version
curl -fsSL https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.sh | sh -s -- v0.4.1

# somewhere you own, no sudo
curl -fsSL https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.sh | INSTALL_DIR="$HOME/.local/bin" sh
```

## One-line installer, Windows

Works on amd64 and arm64, in PowerShell:

```powershell
irm https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.ps1 | iex
```

It downloads `pelt_windows_<arch>.zip`, checks its SHA-256 against
`checksums.txt`, puts `pelt.exe` in `%LOCALAPPDATA%\Programs\pelt` and adds
that folder to your user PATH, so no admin rights are needed. `pelt` works
right away in that window; other open terminals need restarting.

To pick a version or folder, set `PELT_VERSION` (e.g. `v0.4.1`) or
`PELT_INSTALL_DIR` first, or set `PELT_NO_MODIFY_PATH=1` to leave PATH alone:

```powershell
$env:PELT_VERSION = 'v0.4.1'; irm https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.ps1 | iex
```

## Versions

The installers and `go install ...@latest` take the latest release. What each
release added:

| Version | Adds |
|---------|------|
| v0.4.1 | Same features as v0.4.0, with prebuilt binaries (fixes the release build and a flaky test). Use this rather than v0.4.0. |
| v0.4.0 | Flow thresholds, [load stages](usage.md#load-stages) (`-stages`, `-ramp`) in both modes, and [per-second results](usage.md#per-second-results) (Timeline, JSON `series`, `-series` CSV). Has no prebuilt binaries, so the installers cannot fetch it. |
| v0.3.0 | [Thresholds](usage.md#thresholds) and exit code 3 in single-URL mode |
| v0.2.0 | [Flow mode](flows.md): `pelt run` |
| v0.1.0 | Single-URL mode |

To check what you have, run `pelt -version` (v0.4.3 and later). Older builds
don't have it; `pelt -h` on v0.4.0 to v0.4.2 lists `-stages`.

## Prebuilt binary

Every [release](https://github.com/namnguyen3019/pelt/releases) except v0.4.0 has
`pelt_<os>_<arch>.tar.gz` for `linux` and `darwin` (`amd64`, `arm64`),
`pelt_windows_<arch>.zip` for Windows, and a `checksums.txt`. For example:

```sh
curl -fsSLO https://github.com/namnguyen3019/pelt/releases/latest/download/pelt_linux_amd64.tar.gz
tar xzf pelt_linux_amd64.tar.gz pelt
sudo mv pelt /usr/local/bin/
```

## With Go

Needs Go 1.23 or newer. Use the lowercase module path:

```sh
go install github.com/namnguyen3019/pelt/cmd/pelt@latest
```

This puts a `pelt` binary in `$(go env GOPATH)/bin`. To also get the local
test server:

```sh
go install github.com/namnguyen3019/pelt/cmd/testserver@latest
```

## From source

```sh
git clone https://github.com/namnguyen3019/pelt.git
cd pelt
go build -o pelt ./cmd/pelt
sudo mv pelt /usr/local/bin/
```

## Releasing

[`.github/workflows/release.yml`](../.github/workflows/release.yml) tests,
builds all six targets with the version stamped in (`pelt -version`), and
attaches the archives and `checksums.txt` to the release. Either way of
releasing works:

- **On GitHub:** Releases, Draft a new release, pick or create a tag such as
  `v0.5.0`, and Publish. Your title and notes stay; the files appear a few
  minutes later.
- **From a terminal:** push a tag, and the workflow creates the release
  with generated notes:

  ```sh
  git tag v0.5.0
  git push origin v0.5.0
  ```

To fill in a release that has no files (v0.2.0 and v0.4.0 were published
before the workflow handled web releases), open Actions, pick **release**,
**Run workflow**, and enter the tag. It builds the code at that tag and
uploads to the existing release, replacing any files of the same name.

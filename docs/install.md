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
curl -fsSL https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.sh | sh -s -- v0.1.0

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

To pick a version or folder, set `PELT_VERSION` (e.g. `v0.1.0`) or
`PELT_INSTALL_DIR` first, or set `PELT_NO_MODIFY_PATH=1` to leave PATH alone:

```powershell
$env:PELT_VERSION = 'v0.1.0'; irm https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.ps1 | iex
```

## Prebuilt binary

Every [release](https://github.com/namnguyen3019/pelt/releases) has
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

Pushing a tag that starts with `v` runs
[`.github/workflows/release.yml`](../.github/workflows/release.yml), which
tests, builds all six targets and publishes a GitHub Release with the archives
and `checksums.txt`:

```sh
git tag v0.1.0
git push origin v0.1.0
```

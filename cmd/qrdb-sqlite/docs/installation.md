# Installation

## Prerequisites

`qrdb-sqlite` uses `mattn/go-sqlite3`, which requires CGo. Before building:

- **Linux**: `sudo apt install gcc` or `sudo dnf install gcc`
- **macOS**: `xcode-select --install`
- **Windows**: install [TDM-GCC](https://jmeubank.github.io/tdm-gcc/) or use WSL2

Confirm CGo is available:

```sh
go env CGO_ENABLED   # must print 1
gcc --version        # must resolve
```

## Building from Source

```sh
# From the repository root
make build-sqlite-gateway
```

This compiles `cmd/qrdb-sqlite` with CGo enabled and places the binary at
`bin/qrdb-sqlite`.

To build manually:

```sh
cd cmd/qrdb-sqlite
CGO_ENABLED=1 go build -o ../../bin/qrdb-sqlite .
```

## Verify the Build

```sh
bin/qrdb-sqlite version
```

Expected output:

```
qrdb-sqlite v0.x.x (go-qrdb vX.Y.Z)
```

## Pre-Built Binaries

Pre-built binaries for Linux/amd64, Linux/arm64, and macOS/arm64 are published
on the GitHub releases page. CGo is statically linked in the official releases,
so no runtime `gcc` dependency is required for those binaries.

## Troubleshooting CGo Build Failures

| Error | Cause | Fix |
|---|---|---|
| `cgo: C compiler "gcc" not found` | gcc missing | Install gcc (see Prerequisites above) |
| `CGO_ENABLED=0` | CGo disabled globally | `export CGO_ENABLED=1` before building |
| `sqlite3.h: No such file or directory` | Missing SQLite dev headers | `sudo apt install libsqlite3-dev` |
| `exec: "gcc": executable file not found in $PATH` | PATH does not include compiler | Add compiler directory to `$PATH` |

For cross-compilation (`GOARCH=arm64 GOOS=linux`) you need the matching
cross-compiler, e.g. `gcc-aarch64-linux-gnu`.

# Desktop GUI — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

**No `qrdb` verb launches the GUI.** The same binary is both the CLI and the
GUI: invoked with **no arguments** on a machine with a display, it opens the
window; invoked with a verb, it runs the CLI.

## Launching

```bash
qrdb          # opens the GUI when a display is detected and the build has it
qrdb kv get x # runs the CLI, regardless
```

Display detection:

| Platform | GUI launches when |
|---|---|
| Linux | `DISPLAY` or `WAYLAND_DISPLAY` is set |
| Darwin | always, **unless `SSH_TTY` is set** |
| Windows | always |

The Darwin `SSH_TTY` check is the one you will notice: over SSH, a bare `qrdb`
gives you the CLI rather than trying to open a window.

## Building it

```bash
make install-frontend    # npm install for the TypeScript SDK + frontend
make build-frontend      # build the TypeScript SDK + Vite frontend
make build-client        # single binary, CLI + GUI, assets embedded
make dev-gui             # Wails dev mode with hot reload
```

`build-client` embeds every frontend asset with `//go:embed`, so the binary
needs nothing external at runtime.

The GUI requires the `desktop` build tag (and CGO). **Without it the binary is
CLI-only**: asking for the GUI yields `GUIUnavailableError` and the CLI runs
instead.

## The `embedded` transport

The GUI build is also what enables the CLI's in-process transport:

```bash
qrdb --transport embedded kv get hello
```

`embedded` is listed in `--transport`'s values on every build, but it works only
in the desktop/GUI build.

## What the GUI does

- **First run** — a setup wizard: choose embedded or remote server, pick a
  storage backend, configure Raft, configure the encryption barrier, review,
  initialize.
- **Returning** — if the server is sealed it shows the unseal prompt; otherwise
  the full application.
- **Views are capability-gated**: what appears follows what the server reports
  through `admin capabilities`.

## Authentication

**There is no login screen.** Authentication is mTLS at the TLS handshake, and
the identity in the header comes from the client certificate's Subject CN. The
GUI is exactly as privileged as the certificate it presents — no more.

## Parity

The GUI has no `CommandDefinition`s; it is not a verb surface. The bindings
behind it are covered by Go unit tests. Note that the TypeScript SDK's seven
contract suites are **not run in CI** — see
[engineering.md](engineering.md#known-test-gap).

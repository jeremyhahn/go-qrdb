# Desktop GUI — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

The same binary that gives you the CLI also gives you a desktop application, and
the same frontend runs in a browser against a remote server.

## Prerequisites

- A binary built with the GUI: `make build-client`. Without the `desktop` build
  tag there is no GUI, and the binary runs as a CLI.
- A display. On Linux that means `DISPLAY` or `WAYLAND_DISPLAY`; on macOS
  anything that is not an SSH session; on Windows, always.
- **Client certificates.** There is no login screen — authentication is mTLS.

## Launching it

```bash
qrdb            # no arguments, with a display: the GUI opens
qrdb kv get x   # a verb: the CLI runs
```

Over SSH on macOS you get the CLI, because `SSH_TTY` is set. That is deliberate.

## First run

The setup wizard walks you through it:

1. **Server mode** — embedded (a server inside this application) or remote.
2. **Embedded**: storage backend (PebbleDB or memory), Raft configuration if you
   want it, the encryption barrier (Shamir shares and threshold), a summary, then
   initialization.
3. **Remote**: the server address and TLS settings, a summary, done.

After the wizard, if the server is sealed you get an unseal prompt; otherwise
the full application.

## Everyday use

The application shows what the server can actually do — views are gated on the
server's reported capabilities, so a server without vectors has no vector view.
Your identity appears in the header, read from your client certificate.

## In a browser

The same frontend runs against a remote `qrdbd` over its REST API. Nothing
special is needed server-side beyond CORS being enabled, and your browser
needs to present a client certificate as usual.

## Building

```bash
make install-frontend
make build-frontend
make build-client        # single binary, all assets embedded
make dev-gui             # hot reload while developing
```

`build-client` embeds the frontend, so the binary is self-contained — there is
nothing to install alongside it.

## Troubleshooting

**`qrdb` with no arguments printed CLI help instead of opening a window.**
Either the binary was built without the `desktop` tag, or no display was
detected. On macOS over SSH the latter is intentional.

**"GUI unavailable".** The build lacks the `desktop` tag. It requires CGO;
rebuild with `make build-client`.

**`--transport embedded` fails.** The embedded transport is part of the
desktop/GUI build. It is listed in the flag's values on every build but only
works in that one.

**The GUI shows fewer views than you expect.** Views are gated on the server's
capabilities. Check `qrdb admin capabilities`: the feature is either not
compiled in, gated by the run mode, or disabled in config.

**It is stuck on an unseal prompt.** The server is sealed. Unseal it — see
[../barrier-seal/user.md](../barrier-seal/user.md).

**There is no username or password box.** Correct. Authentication is mTLS at the
TLS handshake. If you cannot connect, the problem is your certificate, not a
missing credential prompt.

**You are worried the GUI can do more than the CLI.** It cannot. There is no
GUI-only API; both paths use the same surfaces, and you have exactly the
permissions your certificate's user holds.

**The desktop app is holding your data.** In embedded mode it is: the data
directory and the barrier are on your machine. Treat a desktop deployment as the
single-node server it is, including for backups.

# Trust Bootstrap — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Command group: `qrdb bootstrap` — trust bootstrap for a **fresh client**
establishing CA trust to a remote node.

> **Every subcommand in this group currently fails.** They return a typed
> `PendingBootstrapBackendError`. The underlying TOFU implementation lives in
> go-xkms; qrdb exposes the command shape so the canonical surface is identical
> across `qrdb`, `xkmsctl`, `quicbaasctl` and `platformctl`. This is stated
> here rather than left to be discovered at the prompt.

**Distinct from `qrdb init`**, which is server-side first touch — see
[../firstboot/cli.md](../firstboot/cli.md).

## The modes

| Verb | Mechanism |
|---|---|
| `qrdb bootstrap auto` | pick the strongest available trust mode automatically |
| `qrdb bootstrap dane` | DANE TLSA pinning (RFC 6698) |
| `qrdb bootstrap noise` | Noise XK out-of-band trust handshake |
| `qrdb bootstrap spki` | SPKI SHA-256 pin |
| `qrdb bootstrap direct` | direct CA bundle copy (operator-supplied trust root) |

**None of them takes a flag.** Each accepts arbitrary positional arguments; the
real argument set will be defined by the backend that implements it.

```bash
qrdb bootstrap auto
qrdb bootstrap spki
```

Both exit non-zero with the pending-backend error.

## What works today

The **server side of `direct`** is implemented: a public endpoint serves the CA
bootstrap certificate to a client with no credentials.

```bash
curl -k https://db.example.com/v1/ca/bootstrap -o ca-bootstrap.pem
```

That route exists **only when the deployment configures a bootstrap
certificate**; otherwise it is a 404 rather than an empty answer.

Having obtained a CA bundle by whatever out-of-band means your deployment
supports, use it as `--ca-file` on ordinary commands:

```bash
qrdb --server https://db.example.com --ca-file ca-bootstrap.pem \
     --cert-file client.pem --key-file client-key.pem barrier status
```

You can also confirm a server has been initialized before you have any
credentials:

```bash
curl -k https://db.example.com/v1/config/initialized
```

## Parity

The five mode verbs exist to hold the canonical surface across the stack's
binaries. They have `CommandDefinition`s in the parity suite covering the shape;
there is no behaviour to exercise until the backend lands.

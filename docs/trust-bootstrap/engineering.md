# Trust Bootstrap — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

## Implementation status

**`qrdb bootstrap` is a command-shape stub in this repository.** Every mode
subcommand is constructed by `newBootstrapModeCmd` (`cmd/qrdb/bootstrap.go`)
and its `RunE` returns:

```go
return &PendingBootstrapBackendError{Mode: mode}
```

The source comment states the intent plainly: *"the underlying TOFU
implementation lives in xkms; qrdb exposes the command shape so the canonical
surface is identical across binaries."*

This is recorded here rather than described as working behaviour. An agent
picking up work on trust bootstrap in **this** repo is working on a shape, not
on a protocol.

## Exported surface

```go
// cmd/qrdb — all five modes accept arbitrary args and take no flags
func newBootstrapCmd() *cobra.Command
func newBootstrapModeCmd(mode, short string) *cobra.Command

type PendingBootstrapBackendError struct{ Mode string }
```

Modes registered: `auto`, `dane`, `noise`, `spki`, `direct`. Each is
`cobra.ArbitraryArgs` with **no flags** — the flag set will be defined by the
backend that implements it.

## Server side — what is implemented

| Route | Auth | Registration condition |
|---|---|---|
| `GET /v1/ca/bootstrap` | **public** | only when exactly one source is configured (`caBootstrapSources == 1`) |

This serves the CA bootstrap certificate to a client that has no credentials
yet. It is the server half of the `direct` mode, and it is real.

`GET /v1/config/initialized` is the other public bootstrap-window route — see
[../firstboot/api.md](../firstboot/api.md).

## Invariants

- **Every `qrdb bootstrap` invocation fails** with
  `PendingBootstrapBackendError` naming the mode. It is a typed error, so a
  caller can distinguish "not implemented here" from a network failure.
- **The command shape is the contract.** Its five modes and their names match
  `xkmsctl`, `quicbaasctl` and `platformctl`; changing them here breaks that
  parity even though nothing here executes.
- **One route, one owner, two possible sources.** The bundle comes from
  qrdb's cluster CA (`server.WithCABootstrap`, which qrdbd sets) or from the
  embedding host (`server.WithBootstrapCABundle` / `sdk.WithBootstrapCABundle`,
  a `func(ctx) ([]byte, error)` read per request). go-xkms's xkmsd owns
  first-contact trust and uses the latter. The host supplies data, not a
  route, as for `/health`. Either way only CERTIFICATE blocks are served.
  Configuring both refuses startup with `*rest.CABootstrapConflictError`
  (`sdk.CABootstrapConflictError`); qrdb never picks one.
- **`/v1/ca/bootstrap` does not exist unless configured.** Unconfigured, the
  path is not even claimed. Until v0.25.1-alpha it was claimed
  unconditionally, so a host serving its own bundle there panicked at boot
  with a route collision. An unconfigured
  deployment returns 404 rather than an empty or self-signed bundle.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Any `qrdb bootstrap <mode>` | `PendingBootstrapBackendError{Mode}` | **yes** — no partial or insecure fallback |
| `/v1/ca/bootstrap` with no bootstrap certificate configured | route unregistered, 404 | **yes** |
| `/v1/ca/bootstrap` configured with bytes that include key material | reduced to `CERTIFICATE` blocks (`clustertls.CertificatesOnly`); no key is ever served; input with no certificate registers no route | **yes** |

## Configuration

The bootstrap certificate served by `/v1/ca/bootstrap` is a server
configuration item; see [../configuration/](../configuration/). The client-side
modes have no configuration here because they have no implementation here.

## Test strategy

- **Unit**: each mode subcommand returns `PendingBootstrapBackendError` with the
  right `Mode`, and the command tree registers exactly the five canonical modes.
  That is the whole testable surface, and testing it is what keeps the shape
  from drifting away from the other binaries.
- **Server side**: `/v1/ca/bootstrap` is registered when a bootstrap certificate
  is configured and absent when it is not. The raw response body is asserted to
  carry no `PRIVATE KEY` block, including when the key is handed in with the
  certificate (`pkg/api/rest/ca_bootstrap_nokey_test.go`).

## Owner question

The division is clear — implementation in go-xkms, shape here — but the
**contract between them is not written down in this repo**: which mode names
are binding, what flag set each mode takes once implemented, and whether
`auto`'s ladder order is a qrdb-side or an xkms-side decision. Until that is
settled, this document describes a stub honestly rather than a protocol
aspirationally.

## Integration Target

Run `make integration-test-trust-bootstrap` for this feature. The per-transport accounting row is `test/integration/COVERAGE.md` under `trust-bootstrap`.

# Rate Limiting — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-27
> Conflicts with this document are defects. Fix them here first.

**There is no CLI command for this feature.** The one operator setting is
the per-client-address limit, `transport.client_rate_limit`, in the file
`qrdbd start --config` reads; it has no flag
([user.md](user.md#for-an-operator-limiting-each-client-address)). A `qrdb`
command refused by it fails with the node's rate-limit refusal, naming the
client address and the wait, on every transport.

`qrdbd` has no flag, and `pkg/config`'s YAML schema has no field, to declare
or adjust a per-permission rate-limit `Policy`. Declaring one is a Go-code
decision made by whoever wires the process's `AuthMiddleware` (see
[engineering.md](engineering.md) for the exact call, and
[user.md](user.md) for a worked example). The `auth.invitation_redeem_limit`
keys configure invitation redemption's own buckets, which run outside this
mechanism ([architecture.md](architecture.md#trust-boundaries)).

## Why not

A `Policy` is a capacity decision an operation's owner takes deliberately,
one permission at a time — the same reasoning
[architecture.md](architecture.md) gives for why no permission ships
throttled by default. A flag or YAML field with no real number to put there
is a configuration surface nobody can fill in correctly.

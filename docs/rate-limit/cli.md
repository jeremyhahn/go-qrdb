# Rate Limiting — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
> Conflicts with this document are defects. Fix them here first.

**There is no CLI surface for this feature.**

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

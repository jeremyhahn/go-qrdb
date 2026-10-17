# Rate Limiting — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first.

**There is no CLI surface for this feature.**

`qrdbd` has no flag, and `pkg/config`'s YAML schema has no field, to declare
or adjust a rate-limit `Policy`. Declaring one is a Go-code decision made by
whoever wires the process's `AuthMiddleware` (see
[engineering.md](engineering.md) for the exact call, and
[user.md](user.md) for a worked example).

## Why not

A `Policy` is a capacity decision an operation's owner takes deliberately,
one permission at a time — the same reasoning
[architecture.md](architecture.md) gives for why no permission ships
throttled by default. Exposing it as a flag or a YAML field without first
having a real number to put there would trade "no configuration surface" for
"a configuration surface nobody has a correct value to fill in," which is
not an improvement. A YAML/flag surface is a natural extension once specific
operations have owner-ruled numbers to declare; see the known gap in
[engineering.md](engineering.md).

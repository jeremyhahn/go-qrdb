# Attested Caller — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

## Exported surface

```go
// pkg/auth — the seam go-xkms implements
type PeerAttestationVerifier interface {
	VerifyPeer(ctx context.Context, peer AttestedPeer) (*PeerAttestation, error)
}
type AttestedPeer struct {
	Identity    *Identity          // authenticated, store-enriched; read-only
	Certificate *x509.Certificate  // verified leaf on the connection
	Thumbprint  string             // its canonical SPKI thumbprint
}
type PeerAttestation struct {
	PeerThumbprint string                 // whose evidence was judged
	CollectedAt    time.Time              // when that evidence was collected
	Passed         bool
	Failure        PeerAttestationFailure // None | Absence | Cryptographic
	Reason         string
}

// pkg/auth/middleware — the gate
type PeerAttestationPolicy struct {
	Permissions    []permission.Permission
	MaxEvidenceAge time.Duration
}
func NewPeerAttestationGate(v auth.PeerAttestationVerifier, p PeerAttestationPolicy) (*PeerAttestationGate, error)
func (g *PeerAttestationGate) Gates(perm permission.Permission) bool
func WithPeerAttestationGate(g *PeerAttestationGate) Option

// pkg/auth/wiring
type AuthMiddlewareConfig struct {
	// ...
	PeerAttestation *middleware.PeerAttestationGate
}

// sdk/go/node
func WithPeerAttestationGate(gate *middleware.PeerAttestationGate) NodeOption
```

`sdk/go` re-exports every type above (`sdk.PeerAttestationVerifier`,
`sdk.NewPeerAttestationGate`, …) in `types_peer_attestation.go`.

## Where it runs

`AuthMiddleware.authorize` is the single funnel; the gate is
`checkPeerAttestation`, called:

- after `authorizer.Authorize` returned nil (never on a denied request);
- in the `permission.IdentityGated` branch, which skips RBAC;
- in the sealed-tenant `admin:health` allowance, which also skips RBAC.

The decision record (`auth.authorize`) records the gate's refusal as the
decision's outcome, so the ordinary trail stays complete; the gate's own record
(`auth.peer_attestation`) carries why.

## Invariants

- **Never admits on an error.** Every branch of `PeerAttestationGate.check`
  that is not the final `return nil` is a refusal.
- **A forwarded (on-behalf) identity is refused first**, reason `forwarded`,
  not security-relevant, before the verifier is consulted.
- **The tenant-barrier bootstrap windows are gated too.** Their fallbacks
  treat an attestation refusal as what it is — not an RBAC denial — and return
  it unchanged (`isGateRefusal`, which also covers an unrecordable decision and
  a rate-limit refusal, so neither opens a window either), and every
  fallback GRANT runs the gate before its decision is recorded
  (`grantBootstrapFallback`). The sealed-window path needs this most:
  `authorize` answers a sealed identity before the gate would run.
- **gRPC streams are gated once, at open.**
- **The gate's age bound is independent of the verifier's.**
- **The verdict must name the connection's certificate**
  (`PeerThumbprint == user.ComputeSPKIThumbprint(cert)`), the same SPKI
  thumbprint certificate bindings use.
- **A policy that does not describe a gate is refused at construction**
  (`*PeerAttestationConfigError`: nil verifier, no permissions, an empty
  permission, a non-positive maximum age). Running without the gate the
  composer asked for would be the fail-open reading of that mistake.
- **Classification sits ahead of every rule that could match a wrapped
  verifier error** in `authFailureRules`, because the verifier's error type is
  outside this package's control.

## Error semantics

| Error | Condition | Class | REST | gRPC / unix | QUIC |
|---|---|---|---|---|---|
| `*PeerAttestationRefusedError`, reason `verifier-error` | the verifier returned an error | unavailable | 503 | `UNAVAILABLE` | unavailable |
| `*PeerAttestationRefusedError`, any other reason | see [architecture.md](architecture.md#what-the-gate-checks-independently-of-the-verifier) | unauthorized | 403 | `PERMISSION_DENIED` | permission denied |
| `*audit.GateError` | the refusal could not be recorded under `fail-closed` | audit-unavailable | 503 | `UNAVAILABLE` | unavailable |
| `*PeerAttestationConfigError` | invalid policy at construction | — | — | — | — |

`PeerAttestationRefusedError` carries `Subject`, `TenantID`, `Permission`,
`Reason`, `SecurityRelevant`, `Detail` and the wrapped `Err`.

## Test strategy

**Integration target:** `make integration-test-attested-caller` (no Docker; selection in `test/integration/features.mk`). The feature has no non-Docker integration test yet (the gate needs an injected verifier that a stock `qrdbd` does not carry), so the target runs zero tests and says so; this is a recorded gap, not a pass. Per-transport accounting is the `attested-caller` row of `test/integration/COVERAGE.md`.

- **Fail-closed, case by case** (`pkg/auth/middleware/peer_attestation_test.go`),
  against the real mTLS authenticator and user store: no evidence, stale
  evidence, evidence for a different peer, a verifier error, a nil verdict,
  future-dated evidence, rejected state (absence and cryptographic), an
  inconsistent verdict, and no peer certificate — each asserting the reason,
  the security flag and the failure class. A test of the happy path alone would
  pass against a gate that admits on error.
- The verifier double implements the interface **contract**: evidence cached
  per authenticated identity, stamped with the peer thumbprint and collection
  time it was collected under.
- **RBAC first**: a denied request never reaches the verifier.
- **Every transport entry point** (REST middleware, gRPC unary and stream
  interceptors, `AuthorizeQUIC`, `AuthorizeMCP`, `AuthorizeTenant`) reaches the
  gate through the one option.
- **Audit**: a refusal writes its own `auth.peer_attestation` record with the
  reason and security flag; an admission writes none; an unrecordable refusal
  follows `auth.audit_failure_policy`.
- **Wiring**: `BuildAuthMiddleware` installs the gate it is given.
- Each security property was mutation-checked: removing it fails a named test.

## Observability

- Audit action `auth.peer_attestation`, result `denied`, attributes `reason`,
  `security_relevant`, `detail`.
- Log line `auth/middleware: peer attestation refused` at `ERROR` when
  security-relevant, `WARN` otherwise.

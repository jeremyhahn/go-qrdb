# Trust Anchors — Engineering

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

## Packages

| Package | Role |
|---|---|
| `pkg/trustanchor` | The seam (`Source`, `Registry`), the built-in `KVRegistry`, bundle parsing and chain verification, change notification |
| `pkg/clienttrust` | The client-auth trust pool and issuer-tenant binding that consume anchors |
| `pkg/certenroll` | Bring-your-own certificate verification (`VerifyBYOC`) |
| `pkg/service` | `CertEnrollmentServicer`: the anchor verbs and `EnrollCertificate` |
| `pkg/server/lifecycle` | Builds the registry and wires it to the pool (`Node.WireTrustAnchors`) |

## Exported surface (`pkg/trustanchor`)

```go
type Anchor struct {
	Fingerprint string // lowercase hex SHA-256 of the DER
	Subject     string
	NotBefore   time.Time
	NotAfter    time.Time
	CertPEM     []byte
	AddedAt     time.Time // zero when the store does not record it
}

type Source interface {
	CABundle(ctx context.Context, tenantID string) ([]byte, error)
}

type Registry interface {
	Source
	Add(ctx context.Context, tenantID string, bundlePEM []byte) ([]Anchor, error)
	List(ctx context.Context, tenantID string) ([]Anchor, error)
	Remove(ctx context.Context, tenantID, fingerprint string) error
}

const MaxAnchorsPerTenant = 32

func NewKVRegistry(store kvstore.KVStore, opts ...KVOption) *KVRegistry
func WithClock(now func() time.Time) KVOption
func WithAnchorAuditLogger(l audit.AuditLogger) KVOption
func WithAnchorAuditPolicy(p audit.FailurePolicy) KVOption

func Observe(r Registry, onChange func()) Registry
type Changes struct{ /* lock-free subscriber list */ }
func (c *Changes) Subscribe(fn func())
func (c *Changes) Notify()

func Fingerprint(cert *x509.Certificate) string
func ParseCertificates(bundlePEM []byte) ([]*x509.Certificate, error)
func ParseAnchors(bundlePEM []byte, now time.Time) ([]*x509.Certificate, error)
func EncodePEM(cert *x509.Certificate) []byte
func ChainContainsAnchor(ctx context.Context, src Source, tenantID string, chain []*x509.Certificate) (bool, error)
func VerifyClientCertificate(ctx context.Context, src Source, tenantID string, leaf *x509.Certificate, intermediates []*x509.Certificate, now time.Time) error
func VerifyClientCertificateAgainst(tenantID string, anchors []*x509.Certificate, leaf *x509.Certificate, intermediates []*x509.Certificate, now time.Time) error
```

## Storage (`KVRegistry`)

A tenant's whole anchor set is one record, `__sys:trust-anchor-set`, keyed by
fingerprint, inside `tenant.IdentityNamespace(tenantID)` of the replicated
store the login CA uses. The `__sys:` prefix cannot collide with data-plane
keys, whose database names cannot contain `:`. `Add` validates the whole
bundle with `ParseAnchors` before writing anything, skips anchors already
registered, and refuses a result above `MaxAnchorsPerTenant`. `Add` and
`Remove` apply as one compare-and-swap of the set, so the cap check and the
write cannot race; a store without `kvstore.AtomicKVStore` is refused with
`AtomicStoreRequiredError`, and a swap that keeps losing ends in
`ConcurrentUpdateError` with nothing applied.

With `WithAnchorAuditLogger`, `Add` and `Remove` write `trust_anchor.add` and
`trust_anchor.remove` to the audit trail before the change commits;
`WithAnchorAuditPolicy` (`auth.audit_failure_policy`, fail-closed by default)
decides whether a change the trail cannot record is refused.

## Composition

`lifecycle.BuildServices` creates the node's client-trust change feed before
the certificate provider, so the provider announces each login CA it comes to
hold, and builds the registry once the user and role services are final:

```go
n.ClientTrustChanges = &trustanchor.Changes{}
n.CertProv = cert.NewDefaultProvider(certStore, barrier,
	cert.WithNewCAHook(n.ClientTrustChanges.Notify), ...)
// ... later, over the tenant-routed IAM services:
anchors := infra.TrustAnchors          // server.WithTrustAnchors, via ResolveCryptoOptions
if anchors == nil {
	anchors = trustanchor.NewKVRegistry(certStore,
		trustanchor.WithAnchorAuditLogger(infra.AuditLogger),
		trustanchor.WithAnchorAuditPolicy(infra.AuditFailurePolicy))
}
n.TrustAnchors = trustanchor.Observe(anchors, n.ClientTrustChanges.Notify)
```

A composer then installs it on the same `*clienttrust.Pool` it put on its
listeners:

```go
svcNode.WireTrustAnchors(pool) // pool.SetTrustAnchors(...) + Changes.Subscribe(pool.Invalidate)
go pool.Run(ctx)               // rebuilds the handshake's trust snapshot
```

`Invalidate` only schedules a rebuild on the pool's `Run` loop and never
blocks the notifier; the TLS handshake reads the last snapshot built and never
the store.

`sdk/go/node` and `cmd/qrdbd` both do this. An embedder with its own trust
store passes `server.WithTrustAnchors(myRegistry)`; a store whose anchors are
managed outside qrdb returns `*trustanchor.ExternallyManagedError` from `Add`
and `Remove`, and `CABundle` remains the only method qrdb's trust decisions
call.

## Issuer-tenant binding (`clienttrust.Pool.VerifyIssuer`)

`VerifyIssuer(ctx, tenantID, chain)` receives the verified chain. The issuer
(`chain[1]`) is accepted when it is the cluster CA, a statically trusted CA,
or the claimed tenant's own login CA. Otherwise the chain is accepted only if
`ChainContainsAnchor(ctx, src, tenantID, chain)` finds one of the **claimed
tenant's** anchors in it, byte for byte. Anchors of other tenants are never
consulted.

## Errors

`EmptyBundleError`, `InvalidAnchorError{Index, Reason}`, `TooManyAnchorsError`,
`AnchorNotFoundError`, `ExternallyManagedError`, `InvalidTenantError`,
`StoreError`, `CertificateNotTrustedError`, `NoAnchorsError`,
`BundleTooLargeError`, `AtomicStoreRequiredError`, `ConcurrentUpdateError`.
Their transport classes are rows in `pkg/api/errclass` (see [api.md](api.md)).

## Testing

- `pkg/trustanchor`: parsing rules, per-tenant scoping of
  `ChainContainsAnchor` and verification, the store's limits and failure
  propagation, `Observe` firing only on successful changes.
- `pkg/clienttrust`: one tenant's anchor never vouches for another tenant's
  leaf; removal plus invalidation drops the anchor; a source failure omits
  anchors only.
- `sdk/go/conformance`: on every remote transport, under
  `auth.mtls_binding_policy: required`, a bring-your-own admin logs in under a
  registered anchor, and a certificate from tenant A's anchor naming a tenant B
  account is refused at enrollment and at login.

## Integration Target

Run `make integration-test-trust-anchors` for this feature. It runs
`test/integration/tenant_trust_anchors_cli_test.go`, which drives
`qrdb tenant trust-anchors` on every protocol: add, list and remove, the
32-anchor cap, the typed refusals, cross-tenant confinement, and a
bring-your-own certificate admitted while its anchor is registered and refused
once it is removed.

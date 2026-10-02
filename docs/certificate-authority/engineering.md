# Certificate Authority — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-10

## Exported surface

`sdk/go`:

```go
func (c *Client) Certs() transport.CertClient
```

Composition-plane option (Tier 2):

```go
func WithCertProvider(p cert.CertProvider) ServerOption
```

`pkg/cert`:

```go
type CertProvider interface {
	GenerateClientCert(ctx context.Context, req *GenerateRequest) (*CertBundle, error)
	// ExportPKCS12 refuses with *CertRevokedError when certPEM's serial is revoked.
	ExportPKCS12(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error)
	RevokeCert(ctx context.Context, serial string) (*RevokeResult, error)
	IsRevoked(ctx context.Context, serial string) (bool, error)
	// TenantForSerial reports which tenant owns the issued certificate
	// bearing this serial, and whether this node holds such a record.
	// RevokeCert takes only a serial, and a serial carries no tenant, so
	// this is the only way an authorization layer can establish the TARGET
	// of a revocation rather than assuming the caller's own tenant.
	// found == false is the deny-by-serial case.
	TenantForSerial(ctx context.Context, serial string) (tenantID string, found bool, err error)
	ListCerts(ctx context.Context, tenantID string) ([]*CertRecord, error)
	CACertPEM() []byte
}

// RevokeOutcome distinguishes a revocation of a certificate this node holds
// a CertRecord for from a deny-by-serial revocation of one it does not.
// Both are successes: the marker is written unconditionally, before the
// outcome is even determined.
type RevokeOutcome int

const (
	RevokeOutcomeRecordUpdated RevokeOutcome = iota
	RevokeOutcomeDenyBySerial
)

type RevokeResult struct {
	Serial  string
	Outcome RevokeOutcome
}

// RecordFound is the bool projection of Outcome that every transport
// serializes.
func (r *RevokeResult) RecordFound() bool

type GenerateRequest struct {
	Username string        // sets certificate CN
	TenantID string        // sets certificate OU
	TTL      time.Duration // zero = provider default (1 year)
}

type CertBundle struct {
	CertPEM []byte // PEM-encoded X.509 client certificate
	KeyPEM  []byte // PEM-encoded ECDSA private key
	Serial  string // hexadecimal serial number
}

type CertRecord struct {
	Serial    string    `json:"serial"`
	Username  string    `json:"username"`   // CN
	TenantID  string    `json:"tenantID"`   // OU
	IssuedAt  time.Time `json:"issuedAt"`   // UTC
	ExpiresAt time.Time `json:"expiresAt"`  // UTC
	Revoked   bool      `json:"revoked"`
}

func NewDefaultProvider(kv kvstore.KVStore, barrier seal.EncryptionBarrier, opts ...Option) *DefaultCertProvider
func WithDefaultTTL(d time.Duration) Option

// FormatSerial is the SINGLE definition of the serial format the CA stores
// and compares by (lowercase base-16, unpadded). Issuance, the revocation
// marker and the enforcement lookup all derive their key from it; two of the
// three spelling it independently is how an enforcement check silently
// matches nothing.
func FormatSerial(serial *big.Int) string

// ReapExpiredRevocations drops revocation markers whose certificate has
// already expired, returning the count. RevokeCert calls it on every
// revocation, so the namespace is bounded without a background sweeper.
func (p *DefaultCertProvider) ReapExpiredRevocations(ctx context.Context) (int, error)

// RevocationCache answers the authentication layer's per-request question
// from a bounded-TTL cache over a CertProvider. It satisfies
// auth.RevocationChecker structurally, so pkg/auth does not import pkg/cert.
func NewRevocationCache(provider CertProvider, ttl time.Duration) *RevocationCache
func (c *RevocationCache) IsCertRevoked(ctx context.Context, peer *x509.Certificate) (bool, error)
func (c *RevocationCache) TTL() time.Duration

const DefaultRevocationCacheTTL = 30 * time.Second

// RevocationStateUnavailableError says no answer exists on this node at all
// (the barrier is sealed), as distinct from a lookup that failed. It carries
// the marker method RevocationStateUnavailable() bool, which is how pkg/auth
// recognises it without importing this package.
type RevocationStateUnavailableError struct{ Err error }
```

`pkg/auth`:

```go
// RevocationChecker is declared at the CONSUMER, so pkg/auth does not depend
// on the certificate authority that answers it.
type RevocationChecker interface {
	IsCertRevoked(ctx context.Context, peer *x509.Certificate) (bool, error)
}

func WithRevocationChecker(rc RevocationChecker) MTLSOption

// EnforceRevocation is THE single definition of the three-way policy
// (refuse / fail closed / admit when there is no state to read). Three call
// sites apply it: the mTLS authenticator, the middleware's on-behalf
// forwarding path, and the cluster key plane — the latter two bypass the
// authenticator chain entirely.
func EnforceRevocation(ctx context.Context, rc RevocationChecker, peer *x509.Certificate) error
func NewMTLSAuthenticator(opts ...MTLSOption) *MTLSAuthenticator
func NewMTLSAuthenticatorWithTrustDomain(trustDomain string, opts ...MTLSOption) *MTLSAuthenticator

// RevocationStateUnavailable reports whether err says revocation state could
// not be consulted at all, rather than that a lookup failed.
func RevocationStateUnavailable(err error) bool

var ErrCertificateRevoked = &AuthError{Code: "cert_revoked", ...}

type RevocationCheckError struct{ Err error }
```

`pkg/auth/middleware`:

```go
func WithRevocationChecker(rc auth.RevocationChecker) Option

// RevocationChecker exposes the checker this middleware enforces with, so an
// authorization site OUTSIDE the middleware enforces against the same state
// and shares its cache rather than building a second one. Nil middleware and
// unwired middleware both answer a nil INTERFACE.
func (m *AuthMiddleware) RevocationChecker() auth.RevocationChecker
```

`pkg/clusterkeys` (the third enforcement point):

```go
// revocation is REQUIRED as a parameter and may be nil as a value: nil is
// "not enforced", the posture of a node with no certificate authority wired.
// A parameter rather than an option, so every construction site states which.
func NewReceiver(kd *keydelivery.Receiver, registry *seal.BarrierRegistry,
	placements PlacementGetter, pinnedTrustDomain, selfNodeID string,
	caPool *x509.CertPool, revocation auth.RevocationChecker,
	deliveryCertPEM []byte, logger *slog.Logger) (*Receiver, error)

func NewSender(resolver AdminURLResolver, client *http.Client,
	caPool *x509.CertPool, pinnedTrustDomain string,
	revocation auth.RevocationChecker, logger *slog.Logger) (*Sender, error)

// The leader's two refusals are distinct sentinels because they mean
// different things to an operator: withdraw the node, versus retry.
var ErrRecipientRevoked, ErrRecipientRevocationUnknown error
```

`pkg/auth/wiring` (the single production wiring point):

```go
type AuthMiddlewareConfig struct {
	// ...
	CertProvider       cert.CertProvider // nil = revocation NOT enforced
	RevocationCacheTTL time.Duration     // the window; 0 = no caching
}
```

## Invariants

- **CN is the username, OU is the tenant.** That mapping is what every
  transport's authentication depends on; changing it changes who every existing
  certificate is.
- **The SPIFFE tenant SAN is emitted only under a configured trust domain**
  (`cert.WithTrustDomain`, fed from `auth.spiffe_trust_domain` through
  `lifecycle.Infra.SPIFFETrustDomain`), with kind `user` and the CN as its name.
  With no trust domain the certificate carries no SubjectAltName extension.
- **`GenerateClientCert` never mints a SAN that disagrees with the OU.**
  `clientSPIFFEURIs` round-trips the ID through `spiffe.ParseID` and requires
  field-for-field equality before signing; anything else is
  `*SPIFFEIdentityError` and no certificate or record exists.
- **The ceremony CA sets the Subject and signs no SAN.** `SignCSROptions`
  carries `TenantID` and `Username`; `SignCSROptions.Subject` and `RawSubject`
  are the contract every `CeremonyCA` implements (`CN=Username`,
  `OU=[TenantID, Profile]`, one RDN per attribute), and `LocalCA.SignCSR`
  issues exactly that, dropping every CSR SAN. A missing tenant or username is
  `*ceremony.CertSubjectError`. `signHolderCSR` is the one path that calls a
  CA, and `checkIssuedSubject` refuses a returned certificate whose Subject,
  SANs or public key differ from what was asked (`*ceremony.CertSubjectError`
  naming the field). `auth.HolderProfile` marks officer and custodian
  certificates; `finalizeIdentity` never resolves one against the user store.
- **`auth.spiffe_trust_domain` is validated at load** with
  `spiffe.ValidateTrustDomain`, the same round trip. A refusal is a
  `*spiffe.TrustDomainError` carrying the rejected value and unwrapping to
  `ErrTrustDomainEmpty` or `ErrTrustDomainInvalid`.
- **Client certificates carry `ClientAuth` extended key usage only.**
- **Serial numbers are 128-bit random**, hex-encoded in `CertRecord.Serial`.
- **CA validity 10 years, default client validity 1 year**; `TTL: 0` means the
  provider default.
- **Every mutating operation checks `barrier.IsSealed()` first.** When sealed,
  `GenerateClientCert`, `ExportPKCS12`, `RevokeCert`, `IsRevoked` and
  `ListCerts` all return `BarrierSealedError`.
- **`CACertPEM()` has no barrier check** — the CA certificate is public data and
  is served from cache even while sealed. That is deliberate: a client needs it
  to connect at all.
- **`getCAMaterial()` returns a copy** of the cached key, never the cached key
  itself, so no caller can alias it.
- **Client private key DER is wiped immediately after PEM encoding**; CA key DER
  is wiped on seal and on every error path.
- **Revocation is enforced from replicated state, not a CRL.** Nothing generates
  a CRL and nothing answers OCSP; that is deliberate — see
  [architecture.md](architecture.md).
- **The MARKER is the authoritative revocation state**, and `IsRevoked` reads
  its PRESENCE, never its payload: a marker written by an older build (the
  literal `"1"`) or one whose payload is corrupt still revokes. Reading a
  revocation fails closed.
- **`CertRecord.Revoked` is display-only.** It is what a listing shows; nothing
  consults it for an access-control decision. It is updated best-effort beside
  the marker — a record this node does not hold, or cannot decode, is simply
  not updated.
- **Revocation is a one-way door.** No transport exposes an unrevoke, and the
  only code path that deletes a marker is `ReapExpiredRevocations`, whose
  predicate is "the certificate has already expired" and never anything about
  the caller.
- **Deny-by-serial is retained.** `RevokeCert` writes the marker whether or not
  this node holds a record for the serial, because revoking a peer-issued
  certificate is a real CA operation. It succeeds either way, and the returned
  `RevokeResult.Outcome` reports which case occurred rather than the error
  return, since both are successes.
- **Deny-by-serial is authorized as a CA-operator act.** `RevokeCert` itself is
  unchanged and still writes the marker; what is gated is who may ask for it.
  A serial that resolves to no record names no tenant, so no tenant-local
  caller can show the certificate is theirs — "revoke another tenant's serial"
  and "revoke a serial a peer issued" are the same request — and the marker is
  read by every tenant. The gate therefore requires `certs:revoke:cross`
  (cross-scope, system tenant only) for that branch, and the ordinary
  self-scope `certs:revoke` against the OWNING tenant when the serial resolves.
  See [architecture.md](architecture.md#which-tenant-a-certs-call-may-act-on).
- **The provider stays authorization-free.** `TenantForSerial` takes no
  identity and makes no decision; it reports a fact. The decision lives in
  `pkg/auth/middleware` (`AuthorizeCertTenant`, `AuthorizeCertRevoke`), once,
  and REST, gRPC, QUIC and MCP all call it. Putting the rule in each handler
  is how three of four surfaces end up enforcing it.
- **`ExportPKCS12` consults revocation before encoding.** It parses `certPEM`,
  derives the serial with `FormatSerial`, and refuses with `*CertRevokedError`
  when `IsRevoked` says the serial is revoked. There is no override: an export
  of a revoked bundle needs to be added deliberately, not defaulted. This is
  an operator signal, not a containment control — the caller already supplies
  `certPEM`/`keyPEM`, so the refusal withholds nothing it did not already
  have.
- **The lookup precedes the write.** A store failure while locating the record
  aborts before any marker exists, so a failed revocation never leaves a marker
  behind that nothing reported.
- **Enforcement is on the authentication path, per request** — never a TLS
  handshake callback, which would leave an established QUIC connection
  authorised indefinitely.
- **THREE enforcement points, one policy.** REST's on-behalf forwarding path
  (`pkg/auth/middleware/onbehalf.go`) verifies the forwarding node's
  certificate without running the authenticator chain, so it gates on
  revocation itself via `middleware.WithRevocationChecker`. The cluster key
  plane (`pkg/clusterkeys`) authorizes on routes that are on the REST public
  allowlist and never reach the middleware at all, so it gates on both its
  receiving and its sending side, through a REQUIRED `auth.RevocationChecker`
  parameter on `clusterkeys.NewReceiver` and `clusterkeys.NewSender`. Required
  rather than optional: a nil checker is a legitimate posture for a node with
  no certificate authority wired, and making it a parameter is what forces
  every construction site to say which posture it chose.
  Production wiring passes the SAME checker to all three, so they share one
  cache; `middleware.AuthMiddleware.RevocationChecker` is how a site outside
  the middleware obtains it, rather than building a second cache over the same
  provider and giving that plane its own TTL window. A test in
  `pkg/auth/wiring` drives a forwarded write through `BuildAuthMiddleware` to
  keep that wiring load-bearing: without it, dropping a gate leaves every
  authenticator-path test green.
- **Each added gate ships with its own control test.** For the cluster key
  plane, one test asserts a revoked peer is refused and a second asserts the
  same certificate on the same request is admitted when no checker is wired.
  The pair is what makes the guard, rather than some other check, the reason
  for the refusal — removing the guard flips exactly the first of the two.

## Storage schema

| Key | Value | Purpose |
|---|---|---|
| `__sys:cert_ca/{tenantID}/wrapped_key` | barrier-encrypted CA private key (DER) | CA key material |
| `__sys:cert_ca/{tenantID}/cert_pem` | PEM-encoded CA certificate | CA certificate |
| `__sys:certs/{tenantID}/{serial}` | `CertRecord` JSON | issued certificate records |
| `__sys:certs_revoked/{serial}` | revocation-entry JSON | revocation markers |

The revocation entry is:

```json
{"serial":"<hex>","revoked_at":"<RFC3339>","not_after":"<RFC3339>"}
```

`not_after` is the revoked certificate's expiry, copied from this node's record
at revocation time, and is **absent when this node held no record** — the
deny-by-serial case. Only a marker with an expiry is ever reaped, so a marker
for a serial nobody here can date is permanent. Markers written by an older
build hold the literal `"1"`; they still revoke and are never reaped.

All four are inside the internal namespace and invisible to user KV
enumeration.

## Error semantics

| Error type | Condition | Fails closed? |
|---|---|---|
| `BarrierSealedError` | the barrier is sealed | **yes** — every mutating op |
| `CAKeyNotAvailableError` | CA private key not loaded or destroyed | **yes** |
| `CertNotFoundError` | declared for a serial absent from the store. **Currently constructed nowhere**: `RevokeCert` is deny-by-serial by design (see Known limitations) and reports the "no local record" case through `RevokeResult`, not this error | — |
| `CertRevokedError` | an operation targets a revoked certificate. Produced by `ExportPKCS12` when the supplied certificate's serial is revoked. Authentication-path enforcement is separate and returns `auth.ErrCertificateRevoked` instead | yes |
| `CertGenerateError` | generation failure — wraps the cause | yes |
| `CertExportError` | PKCS#12 export failure — wraps the cause | yes |
| `StoreError` | KVStore failure — wraps the cause | yes |
| `ValidationError` | request field validation | yes |

`CertGenerateError`, `CertExportError` and `StoreError` implement `Unwrap()`, so
`errors.Is` and `errors.As` work through them.

## Permission model

| Operation | Permission |
|---|---|
| Generate certificate | `certs:generate` |
| Export PKCS#12 | `certs:export` |
| Revoke certificate | `certs:revoke` |
| Revoke a serial this node holds no record for (deny-by-serial) | `certs:revoke` **and** `certs:revoke:cross` |
| List certificates | `certs:list` |
| Read certificate metadata | `certs:read` |

`certs:revoke:cross` is **cross-scope**: the RBAC authorizer admits it only for
identities in the system tenant. It is an escalation ON TOP of `certs:revoke`,
not a substitute for it -- so a caller needs both, and the built-in Admin
role grants both.

Every other permission in the table is **`ScopeSelfStrict`**
(`pkg/permission/self_strict_scope.go`), and for generate, revoke and list the
gate re-checks it against the tenant the REQUEST names rather than the tenant
the caller is in. `ScopeSelfStrict` is not lifted by the system-tenant rule, so
the target must be the caller's own tenant for every caller, the system tenant
included. A caller acting inside its own tenant is unaffected. The embedded
transport has no authorization middleware and applies the same rule with
`tenant.Confine` against the caller in its context: `GenerateCert` and
`ListCerts` confine the named tenant, and `RevokeCert` confines the serial's
owner, or the system tenant for a deny-by-serial revocation.

## Configuration

```go
provider := cert.NewDefaultProvider(kvStore, barrier,
	cert.WithDefaultTTL(24*time.Hour))  // override the 1-year default
```

Replacing the provider entirely:

```go
import xkmscert "github.com/jeremyhahn/go-xkms/pkg/cert"

server, err := qrdb.NewServer(
	server.WithCertProvider(xkmscert.NewProvider(xkmsCA)),
)
```

go-xkms adds CRL generation, OCSP responder integration, HSM-backed CA key
storage (TPM2, PKCS#11, cloud KMS), extended certificate profiles with custom
extensions, and certificate transparency logging. **The `CertProvider`
interface is the boundary — go-qrdb never depends on go-xkms directly.**

## Persistence

Records are per tenant and keyed by serial; revocation is a separate key so it
can be checked without loading the record. **Compatibility**: the four storage
key patterns are the on-disk contract for this feature.

## Test strategy

- **Unit**: issuance (CN/OU mapping, TTL default and override, ClientAuth-only
  key usage, 128-bit serial), export, revoke, `IsRevoked`, list, and the
  `BarrierSealedError` path for **each** mutating operation.
- **The SPIFFE SAN is tested against the real authenticator over a real
  verified chain** (`pkg/cert/spiffe_san_test.go`): a certificate minted under a
  trust domain resolves with `tenant_source=spiffe` on a node pinned to that
  domain and is refused on a node pinned to another; one minted with no trust
  domain carries no SubjectAltName and resolves through the OU; a SAN that
  would not parse back to the Subject's tenant and name is refused with nothing
  persisted; and a key enrolled without the SAN still matches its binding after
  re-issue with it.
- **`CACertPEM()` while sealed is its own test** — it must still work.
- **Memory tests**: `getCAMaterial()` returning a copy, client key DER wiped
  after encoding.
- **Error tests** per function, including `Unwrap()` behaviour on the three
  wrapping error types.
- **Revocation ENFORCEMENT is tested end to end**, through the production
  wiring: issue a certificate from a real provider, authenticate a request with
  it, revoke it, and assert the next request is refused and the handler is not
  reached. A test that hands a mapper a revocation error it constructed itself
  verifies the handling and leaves the condition unreachable — which is how
  revocation was recorded, displayed and enforced nowhere.
- **The two failure modes are separately pinned**: a failed lookup refuses the
  request (retryable), and a sealed barrier admits it, so neither can be
  flipped without a test going red.
- **Serial-format agreement is its own test.** The enforcement lookup derives
  its key from a parsed certificate and the CA wrote its record from the
  `big.Int` it minted; if those disagree, every check silently matches nothing
  and every other test still passes.
- **`RevokeResult.Outcome` is tested for both cases in the same test**: revoke
  a serial this node issued and one it does not hold a record for, and assert
  the two `RevokeOutcome` values are distinct. `ExportPKCS12`'s revocation
  refusal is mutation-tested: disabling the check is what proves the test
  would have caught a revoked certificate shipping in a PKCS#12 archive.
- **Tenancy is tested at two layers, on purpose.** What the gate DECIDES is
  settled once, against the real RBAC authorizer, beside the gate. What each
  transport must prove is that it CALLS the gate — every transport reaches the
  same process-wide provider, so a rule enforced on three of four surfaces is a
  rule with a door beside it, and that failure is invisible from inside the
  middleware package.
- **The cross-tenant tests use two tenants over ONE shared store**, with the
  real `DefaultCertProvider` and the real authorizer. A double narrower than
  production would hide this defect rather than catch it: the defect is that
  the real scope check is vacuous, so a test double that "checks the tenant"
  proves the opposite of what it appears to.
- **Each refusal ships with its control.** Own-tenant generate, revoke and list
  keep working, the system tenant still reaches every tenant, and a
  no-middleware deployment is still served — otherwise "deny everything" would
  pass every test above.
- **The transport tests fail the double when the effect runs.** The provider
  used for the QUIC and MCP gate tests calls `t.Fatal` from
  `GenerateClientCert`/`RevokeCert`/`ListCerts`, so a handler that authorizes
  AFTER acting fails rather than passing on the returned error.

## Known limitations

- **No CRL, no OCSP.** Deliberate for this trust domain; external relying
  parties cannot check revocation, which is the principal reason to inject the
  go-xkms provider. See [architecture.md](architecture.md).
- **Reaping is driven by revocations, not by a timer.** A deployment that stops
  revoking keeps its last set of expired markers until the next revocation.
  There is no background sweeper by design (no timers in this package);
  `ReapExpiredRevocations` is exported for an embedder that wants one.
- **`qrdb ca info` and `qrdb ca bundle` are aliases for `list`** — CA-only
  metadata (DN, fingerprint, validity) and a standalone CA+chain bundle export
  are **not yet exposed through the SDK**. The CLI documents this in its own
  help text.

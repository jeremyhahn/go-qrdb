# Certificate Authority — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

## Exported surface

`sdk/go`:

```go
func (c *Client) Certs() transport.CertClient
```

Composition-plane options (Tier 2, `pkg/server`):

```go
func WithCertProvider(p cert.CertProvider) ServerOption
// WithCertEnrollment configures the issue/enrol and trust-anchor service for
// every protocol server (lifecycle.Node.CertEnrollment).
func WithCertEnrollment(svc service.CertEnrollmentServicer) ServerOption
// WithTrustAnchors replaces the built-in per-tenant trust-anchor store.
func WithTrustAnchors(r trustanchor.Registry) ServerOption
```

`pkg/service` — the one service every transport's issue and enrol verbs
delegate to (`cert_enrollment.go`):

```go
type CertEnrollmentServicer interface {
	// SignCertificate signs a CSR for an EXISTING account with its tenant's
	// own Key B CA and binds the certificate's SPKI to the account in the
	// same operation. A CSR for an account that does not exist is refused
	// with *user.UserNotFoundError before anything is signed.
	SignCertificate(ctx context.Context, req *SignCertificateRequest) (*EnrolledCertificate, error)
	// EnrollCertificate binds a bring-your-own certificate to an EXISTING
	// account after verifying it chains to one of the tenant's registered
	// trust anchors and authenticates as exactly that account.
	EnrollCertificate(ctx context.Context, req *EnrollCertificateRequest) (*EnrolledCertificate, error)
	// IssueDelegation has the caller's own tenant CA certify a service
	// principal's CSR with a delegation extension (pkg/auth/delegation).
	// certs:delegate is tenant-only: the system tenant and a delegated
	// caller are refused with *DelegationDeniedError.
	IssueDelegation(ctx context.Context, req *IssueDelegationRequest) (*IssuedDelegation, error)
	AddTrustAnchors(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error)
	ListTrustAnchors(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error)
	RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error
}

type SignCertificateRequest struct {
	TenantID string
	Username string
	CSRPEM   []byte
	TTL      time.Duration // zero = the issuer's default
}

type EnrollCertificateRequest struct {
	TenantID string
	Username string
	CertPEM  []byte // leaf first
	ChainPEM []byte
}

type EnrolledCertificate struct {
	UserID     uint64
	Username   string
	TenantID   string
	CertPEM    []byte
	Serial     string // empty for a bring-your-own certificate
	Thumbprint string
	NotAfter   time.Time
}

type IssueDelegationRequest struct {
	TenantID  string        // empty = the caller's own; any other is refused
	Principal string        // the embedder's service principal
	Scope     []string      // registered permissions, at most delegation.MaxScope
	CSRPEM    []byte        // proof of possession; its Subject is ignored
	TTL       time.Duration // zero = delegation.DefaultLifetime; max delegation.MaxLifetime
}

type IssuedDelegation struct {
	TenantID, Principal string
	Scope               []string
	CertPEM             []byte
	Serial              string
	NotAfter            time.Time
}

func NewCertEnrollmentService(certs cert.CertProvider, users certEnrollmentUsers,
	anchors trustanchor.Registry, opts ...CertEnrollmentOption) *CertEnrollmentService
```

The transports pass the caller's own tenant as `TenantID`; no wire request
carries one.

`pkg/cert`:

```go
type CertProvider interface {
	// IssueClientCert signs a client certificate for req.PublicKey, naming
	// req.Username in req.TenantID, with that tenant's own CA. It never
	// generates a key.
	IssueClientCert(ctx context.Context, req *IssueRequest) (*IssuedCert, error)
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

type IssueRequest struct {
	Username  string           // sets certificate CN
	TenantID  string           // sets certificate OU, and selects the tenant's Key B CA
	PublicKey crypto.PublicKey // the key certified; must pass CheckClientKey
	TTL       time.Duration    // zero = provider default (1 year)
	// Delegation, when set, is written as the critical delegation extension
	// and caps the lifetime at delegation.MaxLifetime; Username must then be
	// "qrdb-service:" plus the principal and TenantID the grant's tenant.
	Delegation *delegation.Grant
}

type IssuedCert struct {
	CertPEM  []byte    // PEM-encoded X.509 client certificate
	Serial   string    // hexadecimal serial number
	NotAfter time.Time // end of the validity window
}

// ParseCSR decodes a PEM certificate signing request and returns its public
// key, after checking the request's own signature and the key (CheckClientKey).
// Nothing else in the request is honoured.
func ParseCSR(csrPEM []byte) (crypto.PublicKey, error)

// CheckClientKey accepts ECDSA P-256/P-384/P-521, RSA of at least 2048 bits,
// and Ed25519; anything else is *InvalidCSRError.
func CheckClientKey(pub crypto.PublicKey) error

type CertRecord struct {
	Serial    string    `json:"serial"`
	Username  string    `json:"username"`   // CN
	TenantID  string    `json:"tenantID"`   // OU
	IssuedAt  time.Time `json:"issuedAt"`   // UTC
	ExpiresAt time.Time `json:"expiresAt"`  // UTC
	Revoked   bool      `json:"revoked"`
	// Delegation is the grant a delegation certificate carries; nil otherwise.
	Delegation *delegation.Grant `json:"delegation,omitempty"`
}

func NewDefaultProvider(kv kvstore.MultiGuardedBatchKVStore, barrier seal.EncryptionBarrier, opts ...ProviderOption) *DefaultCertProvider
func WithDefaultTTL(d time.Duration) ProviderOption

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
- **Every client certificate carries the tenant's SPIFFE ID SAN**,
  `spiffe://<tenant>/user/<CN>` (a delegation, `spiffe://<tenant>/svc/<principal>`):
  the tenant is the trust domain. `cert.WithTrustDomain` names the cluster
  trust domain (fed from the effective `auth.cluster_trust_domain` through
  `lifecycle.Infra.ClusterTrustDomain`), and no tenant may be it
  (`auth.TenantPrincipalURIs`). A provider with no valid cluster trust domain
  refuses to issue (`*SPIFFEIdentityError`) before the CA key is touched.
- **No server-side client key generation.** `IssueClientCert` certifies the
  public key it is handed and never sees a private key. Test code that needs a
  key and a certificate uses
  `pkg/cert/certtest.Generate`, which makes the key locally and calls
  `IssueClientCert`.
- **Only a CSR's key is used.** `ParseCSR` checks the request's signature
  (proof of possession) and the key type, and the certificate's Subject and
  SAN come from the account the issuer names, never from the CSR.
- **Issue and enrol bind atomically.** `SignCertificate` and
  `EnrollCertificate` require the account to exist and add the certificate's
  SPKI thumbprint to it (`AddCertBinding`, with the certificate's expiry). If
  the binding fails after a certificate was signed, `SignCertificate` revokes
  that serial and returns `*service.CertBindingFailedError`, whose `RevokeErr`
  says when the revocation failed too.
- **A delegation is minted only by its own tenant's CA, for its own tenant.**
  `IssueDelegation` confines the request to the caller's tenant
  (`tenant.Confine`), refuses the system tenant and a caller authenticated by a
  delegation (`*DelegationDeniedError`), refuses a built-in role name as the
  principal, an unregistered permission, or a lifetime above
  `delegation.MaxLifetime` (`*DelegationRequestError`), and writes the
  `cert.delegate` audit event before `IssueClientCert` signs. The extension is
  always critical, so a verifier that does not understand it rejects the
  certificate rather than treating it as a plain login.
- **`IssueClientCert` never mints a SAN that disagrees with the OU.**
  `clientSPIFFEURIs` builds the SAN from the same tenant and name as the
  Subject with `auth.TenantPrincipalURIs`, which validates the ID
  (`spiffe.ID.URI`) before signing; anything else is `*SPIFFEIdentityError`
  and no certificate or record exists.
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
- **`auth.cluster_trust_domain` is validated at load** with
  `spiffe.ValidateTrustDomain`, the same round trip. A refusal is a
  `*spiffe.TrustDomainError` carrying the rejected value and unwrapping to
  `ErrTrustDomainEmpty` or `ErrTrustDomainInvalid`.
- **Client certificates carry `ClientAuth` extended key usage only.**
- **Serial numbers are 128-bit random**, hex-encoded in `CertRecord.Serial`.
- **CA validity 10 years, default client validity 1 year**; `TTL: 0` means the
  provider default.
- **Every mutating operation checks `barrier.IsSealed()` first.** When sealed,
  `IssueClientCert`, `RevokeCert`, `IsRevoked`, `ListCerts` and `CACertPEMs`
  all return `BarrierSealedError`.
- **`getCAMaterial()` resolves a `cakey.Provider` and returns its
  `crypto.Signer`**, never raw key bytes: it signs in place, inside the
  provider, so no caller holds CA key material to alias in the first place.
  The built-in per-tenant `cakey.SoftwareProvider` holds its key in a
  `secure.Secret` (go-enclave protected memory) whenever established,
  opening it into an mlocked `secure.Buffer` only for the duration of one
  `Sign` call, and destroys the `Secret` outright on seal; a provider
  resolved from an injected `cakey.ProviderFactory`
  (`WithKeyProviderFactory`) manages its own key custody entirely.
- **Revocation is enforced from replicated state, not a CRL.** Nothing generates
  a CRL and nothing answers OCSP; that is deliberate — see
  [architecture.md](architecture.md).
- **The MARKER is the authoritative revocation state**, and `IsRevoked` reads
  its PRESENCE, never its payload: a marker whose payload is the literal `"1"`
  or is corrupt still revokes. Reading a
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
- **Deny-by-serial is authorized as a CA-operator act.** `RevokeCert` writes the
  marker; what is gated is who may ask for it.
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
| `__sys:cert_ca/{tenantID}/material` | SEC1 DER-encoded EC private key, raw | the CA private key, written together with `certificate` below in one atomic `PutAllIfAllUnchanged` batch — no per-record encryption of its own; see "Trust boundaries" |
| `__sys:cert_ca/{tenantID}/certificate` | DER-encoded X.509 certificate, raw | the CA's own self-signed certificate; public data, read without ever touching `material` |
| `__sys:cert_ca_pub/{tenantID}/certificate` | the CA certificate, DER | the published copy of an isolated tenant's CA certificate (below) |
| `__sys:certs/{tenantID}/{serial}` | `CertRecord` JSON | issued certificate records |
| `__sys:certs_revoked/{serial}` | revocation-entry JSON | revocation markers |

**An isolated tenant's records live in its home store.** For a tenant whose
records the node's resolver homes — every `barrier`, `volume` and `raft`
tenant, whatever its isolation policy — (`tenant.HomeLocator`, see
`docs/multi-tenancy/`), `material`, `certificate`, the `certs/` records and the
revocation markers of the certificates its CA signed are in the tenant's home
store, under the same keys. The system store holds only
`__sys:cert_ca_pub/{tenantID}/certificate`, written before the CA first signs,
and chain verification reads only that copy. Every other tenant's records, and
every node and platform certificate's, are in the system store.

The revocation entry is:

```json
{"serial":"<hex>","revoked_at":"<RFC3339>","not_after":"<RFC3339>"}
```

`not_after` is the revoked certificate's expiry, copied from this node's record
at revocation time, and is **absent when this node held no record** — the
deny-by-serial case. Only a marker with an expiry is ever reaped, so a marker
for a serial nobody here can date is permanent. A marker whose payload is the
literal `"1"` carries no expiry; it revokes and is never reaped.

Every key above is inside the internal namespace and invisible to user KV
enumeration.

## Error semantics

| Error type | Condition | Fails closed? |
|---|---|---|
| `BarrierSealedError` | the barrier is sealed | **yes** — every mutating op |
| `CAKeyNotAvailableError` | CA private key not loaded or destroyed | **yes** |
| `CertNotFoundError` | declared for a serial absent from the store. **Constructed nowhere**: `RevokeCert` is deny-by-serial by design (see Known limitations) and reports the "no local record" case through `RevokeResult`, not this error | — |
| `CertRevokedError` | declared for an operation on a revoked certificate. **Constructed nowhere**: authentication-path enforcement returns `auth.ErrCertificateRevoked` | — |
| `CertGenerateError` | signing failure — wraps the cause | yes |
| `CertExportError` | `EncodePKCS12` failure, in the CLI's local `ca export-bundle` — wraps the cause | yes |
| `StoreError` | KVStore failure — wraps the cause | yes |
| `ValidationError` | request field validation | yes |
| `InvalidCSRError` | a CSR that does not parse or verify, or a key `CheckClientKey` refuses | yes |
| `service.CertBindingFailedError` | the certificate could not be bound to its account; a signed serial was revoked | yes |

`CertGenerateError`, `CertExportError` and `StoreError` implement `Unwrap()`, so
`errors.Is` and `errors.As` work through them.

## Permission model

| Operation | Permission |
|---|---|
| Issue a certificate from a CSR | `certs:issue` |
| Enrol a bring-your-own certificate | `certs:enroll` |
| Issue a delegation to a service principal | `certs:delegate` (tenant-only) |
| Revoke certificate | `certs:revoke` |
| Revoke a serial this node holds no record for (deny-by-serial) | `certs:revoke` **and** `certs:revoke:cross` |
| List certificates | `certs:list` |
| Read certificate metadata | `certs:read` |

`certs:revoke:cross` is **cross-scope**: the RBAC authorizer admits it only for
identities in the system tenant. It is an escalation ON TOP of `certs:revoke`,
not a substitute for it -- so a caller needs both, and the built-in Admin
role grants both.

Every other permission in the table is **`ScopeSelfStrict`**
(`pkg/permission/self_strict_scope.go`), and so are `trust_anchors:add`,
`trust_anchors:list` and `trust_anchors:remove`. For revoke and list the gate
re-checks the permission against the tenant the REQUEST reaches rather than the
tenant the caller is in; issue and enrol are checked against the caller's own
tenant, the only one they can act on. The built-in SO and Admin roles hold
`certs:issue` and `certs:enroll`.
`ScopeSelfStrict` is not lifted by the system-tenant rule, so the target must
be the caller's own tenant for every caller, the system tenant included. The embedded transport has
no authorization middleware and applies the same rule with `tenant.Confine`
against the caller in its context: `IssueCert` and `EnrollCert` act on the
caller's own tenant, `ListCerts` confines the named tenant, and `RevokeCert`
confines the serial's owner, or the system tenant for a deny-by-serial
revocation.

## Configuration

```go
provider := cert.NewDefaultProvider(kvStore, barrier,
	cert.WithDefaultTTL(24*time.Hour))  // override the 1-year default
```

Replacing the provider entirely:

```go
import xkmscert "github.com/jeremyhahn/go-xkms/pkg/cert"

srv := server.New(cfg, kvSvc, adminSvc,
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
  key usage, 128-bit serial), revoke, `IsRevoked`, list, and the
  `BarrierSealedError` path for **each** mutating operation. CSR handling
  (signature, each accepted and refused key type) and the enrolment service
  (missing account, refused CSR, binding failure revoking the serial).
- **The SPIFFE SAN is tested against the real authenticator over a real
  verified chain** (`pkg/cert/spiffe_san_test.go`). A tenant certificate
  names its tenant twice — the first subject OU and the SPIFFE ID SAN — and the
  authenticator accepts it only when both are present and agree. A certificate
  minted for a tenant carries both and resolves to that tenant; one whose OU
  names the cluster trust domain is refused; issuing with no cluster trust
  domain is refused before signing, with nothing persisted; a SAN
  that would not parse back to the Subject's tenant and name is refused with
  nothing persisted; and a key enrolled under one certificate still matches its
  binding after re-issue under the same CN and tenant.
- **Memory tests**: CA key wiping is `pkg/cakey/software_test.go`'s
  responsibility, since the key lives there;
  `pkg/cert/key_provider_test.go` proves the injected-provider path signs
  through the provider and never writes software key material at all.
- **Error tests** per function, including `Unwrap()` behaviour on the three
  wrapping error types.
- **Revocation ENFORCEMENT is tested end to end**, through the production
  wiring: issue a certificate from a real provider, authenticate a request with
  it, revoke it, and assert the next request is refused and the handler is not
  reached. A test that hands a mapper a revocation error it constructed itself
  verifies the handling and leaves the condition unreachable.
- **The two failure modes are separately pinned**: a failed lookup refuses the
  request (retryable), and a sealed barrier admits it, so neither can be
  flipped without a test going red.
- **Serial-format agreement is its own test.** The enforcement lookup derives
  its key from a parsed certificate and the CA wrote its record from the
  `big.Int` it minted; if those disagree, every check silently matches nothing
  and every other test still passes.
- **`RevokeResult.Outcome` is tested for both cases in the same test**: revoke
  a serial this node issued and one it does not hold a record for, and assert
  the two `RevokeOutcome` values are distinct.
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
- **Each refusal ships with its control.** Own-tenant issue, revoke and list
  keep working, and a no-middleware deployment is still served — otherwise "deny everything" would
  pass every test above.
- **Delegation issuance is tested on every transport through the real CLI**
  (`TestCADelegate_CLI_PerProtocol`, `make integration-test-certificate-authority`):
  the tenant's own CA issues it with the principal and scope in a critical
  extension; it authenticates holding its scope intersected with the
  principal's own grants and writes the tenant's own database; it is refused
  when it names another principal, when another tenant's CA signed it, on
  another tenant's data, once revoked, once expired, and when the tenant's own
  CA signed it for longer than 24 hours (the same certificate at exactly 24
  hours is admitted); and the tenant cannot issue one for a built-in role,
  beyond the maximum lifetime, or for an unknown permission. The node's Key B
  is a `WithCertKeyProviderFactory` the test holds, which is how it signs the
  over-long delegation the CA's own issuance refuses to. The verifier-side unit tests are listed in
  [../identity-rbac/engineering.md](../identity-rbac/engineering.md#test-strategy).
- **The transport tests fail the double when the effect runs.** The provider
  used for the QUIC and MCP gate tests calls `t.Fatal` from
  `IssueClientCert`/`RevokeCert`/`ListCerts`, so a handler that authorizes
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
  are **not exposed through the SDK**. The CLI documents this in its own
  help text.

## Integration Target

Run `make integration-test-certificate-authority` for this feature. The per-transport accounting row is `test/integration/COVERAGE.md` under `certificate-authority`.

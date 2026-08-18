# Certificate Authority — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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
	ExportPKCS12(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error)
	RevokeCert(ctx context.Context, serial string) error
	IsRevoked(ctx context.Context, serial string) (bool, error)
	ListCerts(ctx context.Context, tenantID string) ([]*CertRecord, error)
	CACertPEM() []byte
}

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
```

## Invariants

- **CN is the username, OU is the tenant.** That mapping is what every
  transport's authentication depends on; changing it changes who every existing
  certificate is.
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
- **Revocation is a blocklist, not a CRL.** `__sys:certs_revoked/{serial}` = `"1"`.
  Nothing generates a CRL and nothing answers OCSP.

## Storage schema

| Key | Value | Purpose |
|---|---|---|
| `__sys:cert_ca/{tenantID}/wrapped_key` | barrier-encrypted CA private key (DER) | CA key material |
| `__sys:cert_ca/{tenantID}/cert_pem` | PEM-encoded CA certificate | CA certificate |
| `__sys:certs/{tenantID}/{serial}` | `CertRecord` JSON | issued certificate records |
| `__sys:certs_revoked/{serial}` | `"1"` | revocation blocklist |

All four are inside the internal namespace and invisible to user KV
enumeration.

## Error semantics

| Error type | Condition | Fails closed? |
|---|---|---|
| `BarrierSealedError` | the barrier is sealed | **yes** — every mutating op |
| `CAKeyNotAvailableError` | CA private key not loaded or destroyed | **yes** |
| `CertNotFoundError` | serial not in the store | yes |
| `CertRevokedError` | operation targets a revoked certificate | **yes** |
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
| List certificates | `certs:list` |
| Read certificate metadata | `certs:read` |

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
- **`CACertPEM()` while sealed is its own test** — it must still work.
- **Memory tests**: `getCAMaterial()` returning a copy, client key DER wiped
  after encoding.
- **Error tests** per function, including `Unwrap()` behaviour on the three
  wrapping error types.

## Known limitations

- **No CRL, no OCSP.** External relying parties cannot check revocation. This is
  the principal reason to inject the go-xkms provider.
- **`qrdb ca info` and `qrdb ca bundle` are aliases for `list`** — CA-only
  metadata (DN, fingerprint, validity) and a standalone CA+chain bundle export
  are **not yet exposed through the SDK**. The CLI documents this in its own
  help text.

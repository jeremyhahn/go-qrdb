<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Certificate Management

The `pkg/cert/` package provides a pluggable `CertProvider` interface for client certificate lifecycle management. go-qrdb is a key-value store, not a PKI system -- certificate management uses a minimal default implementation. Library consumers (e.g., go-xkms) provide production-grade CA capabilities via `server.WithCertProvider()`.

## CertProvider Interface

Source: `pkg/cert/provider.go`

```go
type CertProvider interface {
    // GenerateClientCert creates a client certificate signed by the CA.
    GenerateClientCert(ctx context.Context, req *GenerateRequest) (*CertBundle, error)

    // ExportPKCS12 exports a certificate and key as PKCS#12 (.p12).
    ExportPKCS12(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error)

    // RevokeCert marks a certificate serial as revoked.
    RevokeCert(ctx context.Context, serial string) error

    // IsRevoked checks whether a certificate serial is revoked.
    IsRevoked(ctx context.Context, serial string) (bool, error)

    // ListCerts lists all issued client certificates for a tenant.
    ListCerts(ctx context.Context, tenantID string) ([]*CertRecord, error)

    // CACertPEM returns the PEM-encoded CA certificate.
    CACertPEM() []byte
}
```

## Types

Source: `pkg/cert/types.go`

```go
type GenerateRequest struct {
    Username string        // Sets certificate CN
    TenantID string        // Sets certificate OU
    TTL      time.Duration // Certificate validity (zero = provider default)
}

type CertBundle struct {
    CertPEM []byte // PEM-encoded X.509 client certificate
    KeyPEM  []byte // PEM-encoded ECDSA private key
    Serial  string // Hexadecimal serial number
}

type CertRecord struct {
    Serial    string    `json:"serial"`    // Hexadecimal serial number
    Username  string    `json:"username"`  // Certificate CN
    TenantID  string    `json:"tenantID"`  // Certificate OU
    IssuedAt  time.Time `json:"issuedAt"`  // Issue timestamp (UTC)
    ExpiresAt time.Time `json:"expiresAt"` // Expiration timestamp (UTC)
    Revoked   bool      `json:"revoked"`   // Whether the cert has been revoked
}
```

## DefaultCertProvider

Source: `pkg/cert/default_provider.go`

The default implementation generates and manages ECDSA P-256 client certificates using Go standard library cryptography. The CA private key is encrypted at rest via the EncryptionBarrier and held in secure memory while unsealed.

### Cryptographic Properties

| Property | Value |
|----------|-------|
| CA Key Algorithm | ECDSA P-256 |
| Client Key Algorithm | ECDSA P-256 |
| CA Validity | 10 years |
| Default Client Validity | 1 year |
| Client ExtKeyUsage | ClientAuth only |
| Serial Number Size | 128-bit random |
| PKCS#12 Export | Via `software.sslmate.com/src/go-pkcs12` |

### Storage Schema

| Key Pattern | Value | Purpose |
|-------------|-------|---------|
| `__sys:cert_ca/{tenantID}/wrapped_key` | Barrier-encrypted CA private key (DER) | CA key material |
| `__sys:cert_ca/{tenantID}/cert_pem` | PEM-encoded CA certificate | CA certificate |
| `__sys:certs/{tenantID}/{serial}` | CertRecord JSON | Issued certificate records |
| `__sys:certs_revoked/{serial}` | `"1"` | Revocation blocklist |

### CA Key Lifecycle

The CA key follows the barrier seal/unseal lifecycle:

```
Bootstrap (barrier unsealed)
  ├─ Generate ECDSA P-256 CA key pair
  ├─ barrier.Encrypt(caKeyDER) → wrapped blob
  ├─ Store wrapped blob at __sys:cert_ca/{tenantID}/wrapped_key
  ├─ Store CA cert PEM at __sys:cert_ca/{tenantID}/cert_pem
  └─ Cache CA key in memory

Unseal
  ├─ Load wrapped blob from KVStore
  ├─ barrier.Decrypt(wrappedBlob) → caKeyDER
  └─ Cache CA key in memory

Seal
  ├─ memguard.WipeBytes(caKey) → zero memory
  ├─ nil CA key and cert references
  └─ All operations return BarrierSealedError

Generate Certificate (barrier unsealed)
  ├─ getCAMaterial() → copy of cached CA key + cert
  ├─ Generate ECDSA P-256 client key pair
  ├─ x509.CreateCertificate(CA signs client)
  ├─ Persist CertRecord to KVStore
  ├─ memguard.WipeBytes(clientKeyDER) after PEM encoding
  └─ Return CertBundle{CertPEM, KeyPEM, Serial}
```

### Memory Protection

- CA private key DER bytes are wiped via `memguard.WipeBytes()` on seal and on error paths
- Client private key DER bytes are wiped immediately after PEM encoding
- `getCAMaterial()` returns a copy of the key to prevent aliasing
- CA cert PEM is public data and is not wiped on seal

### Barrier Integration

All mutating operations check `p.barrier.IsSealed()` before proceeding. When the barrier is sealed:

- `GenerateClientCert` returns `BarrierSealedError`
- `ExportPKCS12` returns `BarrierSealedError`
- `RevokeCert` returns `BarrierSealedError`
- `IsRevoked` returns `BarrierSealedError`
- `ListCerts` returns `BarrierSealedError`
- `CACertPEM()` returns the cached PEM (public data, no barrier check)

### Configuration

```go
provider := cert.NewDefaultProvider(
    kvStore,
    barrier,
    cert.WithDefaultTTL(24 * time.Hour), // Override default 1-year TTL
)
```

## CLI Commands

Source: `cmd/qrdb/certs.go`

```
qrdb certs generate --username <username> [--tenant-id <id>] [--ttl <duration>] --out <dir>
qrdb certs export --username <username> --format p12 --passphrase <pass> --out <file>
qrdb certs revoke --serial <serial>
qrdb certs list [--tenant-id <id>]
```

### Examples

```bash
# Generate a client certificate for user "alice"
qrdb certs generate --username alice --out /tmp/alice-certs/
# Output: /tmp/alice-certs/alice-cert.pem, /tmp/alice-certs/alice-key.pem

# Export as PKCS#12 for browser import
qrdb certs export --username alice --format p12 --passphrase changeme --out /tmp/alice.p12

# Revoke by serial number
qrdb certs revoke --serial 1a2b3c4d5e6f

# List all issued certificates
qrdb certs list --tenant-id default
```

## API Endpoints

### REST

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/certs/generate` | Generate client certificate |
| `POST` | `/v1/certs/export` | Export certificate as PKCS#12 |
| `POST` | `/v1/certs/revoke` | Revoke certificate by serial |
| `GET` | `/v1/certs` | List issued certificates (query: `tenantId`) |

### gRPC

```protobuf
rpc GenerateClientCert(GenerateClientCertRequest) returns (CertBundle);
rpc ExportPKCS12(ExportPKCS12Request) returns (ExportPKCS12Response);
rpc RevokeCert(RevokeCertRequest) returns (google.protobuf.Empty);
rpc ListCerts(ListCertsRequest) returns (ListCertsResponse);
```

### MCP Tools

| Tool | Description |
|------|-------------|
| `qrdb_certs_generate` | Generate a client certificate |
| `qrdb_certs_export` | Export certificate as PKCS#12 |
| `qrdb_certs_revoke` | Revoke a certificate |
| `qrdb_certs_list` | List issued certificates |

## Required Permissions

| Operation | Permission |
|-----------|------------|
| Generate certificate | `certs:generate` |
| Revoke certificate | `certs:revoke` |
| List certificates | `certs:list` |

See [permissions/README.md](../permissions/README.md) for the full permission reference.

## Error Types

Source: `pkg/cert/errors.go`

| Error Type | Condition |
|------------|-----------|
| `CertNotFoundError` | Certificate serial does not exist in the store |
| `CertRevokedError` | Operation targets a revoked certificate |
| `CertGenerateError` | Certificate generation failure (wraps cause) |
| `CertExportError` | PKCS#12 export failure (wraps cause) |
| `CAKeyNotAvailableError` | CA private key not loaded or destroyed |
| `BarrierSealedError` | Encryption barrier is sealed |
| `StoreError` | KVStore operation failure (wraps cause) |
| `ValidationError` | Request field validation failure |

`CertGenerateError`, `CertExportError`, and `StoreError` implement `Unwrap()` for `errors.Is` and `errors.As` compatibility.

## go-xkms Integration

Library consumers can replace the default provider with a production-grade implementation:

```go
import xkmscert "github.com/jeremyhahn/go-xkms/pkg/cert"

server, err := qrdb.NewServer(
    server.WithCertProvider(xkmscert.NewProvider(xkmsCA)),
    // ...
)
```

go-xkms provides:

- Full CRL (Certificate Revocation List) generation
- OCSP responder integration
- HSM-backed CA key storage (via TPM2, PKCS#11, cloud KMS)
- Extended certificate profiles with custom extensions
- Certificate transparency logging

The `CertProvider` interface is the boundary -- go-qrdb never depends on go-xkms directly.

## SDK Usage

```go
import sdk "github.com/jeremyhahn/go-qrdb/sdk/go"

client, err := sdk.NewClient(
    sdk.WithTransport("grpc"),
    sdk.WithAddress("localhost:63003"),
    sdk.WithCertFile("/etc/qrdb/admin-cert.pem"),
    sdk.WithKeyFile("/etc/qrdb/admin-key.pem"),
)
defer client.Close()

// Generate a certificate for a new user
bundle, err := client.Certs().Generate(ctx, &transport.GenerateCertRequest{
    Username: "alice",
    TenantID: "default",
    TTL:      "720h",
})

// Write certificate files
os.WriteFile("alice-cert.pem", bundle.CertPEM, 0600)
os.WriteFile("alice-key.pem", bundle.KeyPEM, 0600)
```

## See Also

- [users/README.md](../users/README.md) -- User entities identified by certificates
- [auth/README.md](../auth/README.md) -- mTLS authentication extracting identity from certs
- [seal/README.md](../seal/README.md) -- EncryptionBarrier protecting CA key material
- [permissions/README.md](../permissions/README.md) -- Certificate management permissions
- [roles/README.md](../roles/README.md) -- Roles controlling certificate operation access

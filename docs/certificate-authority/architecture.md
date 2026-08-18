# Certificate Authority — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Issuing, exporting and revoking the client certificates that authenticate every
caller. Authentication is mTLS throughout the store, so this is the feature that
mints identity.

## Problem

Every transport authenticates with mTLS, so a client with no certificate cannot
do anything. Something has to issue them — and it has to do so without becoming
the place a production PKI is expected to live.

## Shape chosen

A narrow `CertProvider` interface with a **deliberately modest default
implementation**, and a composition seam for a real one.

```go
type CertProvider interface {
	GenerateClientCert(ctx context.Context, req *GenerateRequest) (*CertBundle, error)
	ExportPKCS12(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error)
	RevokeCert(ctx context.Context, serial string) error
	IsRevoked(ctx context.Context, serial string) (bool, error)
	ListCerts(ctx context.Context, tenantID string) ([]*CertRecord, error)
	CACertPEM() []byte
}
```

`DefaultCertProvider` issues ECDSA P-256 client certificates with the Go
standard library, keeps the CA private key barrier-encrypted at rest and in
`memguard` while unsealed, and tracks revocation as a blocklist.

**go-xkms provides the production alternative** — full CRL generation, OCSP,
HSM-backed CA keys (TPM2, PKCS#11, cloud KMS), extended certificate profiles,
certificate transparency logging — injected through `WithCertProvider`. **The
`CertProvider` interface is the boundary; go-qrdb never depends on go-xkms
directly.**

### Alternatives rejected

- **Shipping a full PKI here.** Rejected: CRL distribution, OCSP responders,
  profile management and CT logging are a product in themselves and they belong
  with the hardware crypto in go-xkms. What qrdb needs is enough CA to
  bootstrap its own mTLS.
- **A revocation *list* rather than a blocklist.** The default provider records
  revocation as a key per serial (`__sys:certs_revoked/{serial}`) and does not
  generate a CRL. That is a real limitation, and it is the main reason a
  deployment with external relying parties wants the go-xkms provider.
- **Leaving the CA key outside the barrier.** Rejected: the CA key is the most
  valuable secret in the system. It is barrier-encrypted at rest and destroyed
  in memory on seal.

## Cryptographic properties

| Property | Value |
|---|---|
| CA key algorithm | ECDSA P-256 |
| Client key algorithm | ECDSA P-256 |
| CA validity | 10 years |
| Default client validity | 1 year |
| Client `ExtKeyUsage` | **ClientAuth only** |
| Serial number | 128-bit random |
| PKCS#12 export | `software.sslmate.com/src/go-pkcs12` |

Client certificates carry `ClientAuth` and nothing else — a certificate issued
here cannot be used to impersonate a server.

## Identity mapping

The certificate **is** the identity:

- **CN** → the username.
- **OU** → the tenant ID.
- The **SPKI thumbprint** is what a user record binds to.

That is why issuance is a privileged operation and why revocation matters: there
is no password to change.

## CA key lifecycle

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

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| `CertProvider` interface, `DefaultCertProvider` | `pkg/cert` | go-qrdb |
| At-rest protection of the CA key | `pkg/seal` — see [../barrier-seal/](../barrier-seal/) | go-qrdb |
| Certificate-to-user binding | `pkg/user` — see [../identity-rbac/](../identity-rbac/) | go-qrdb |
| Ceremony CA adapter | `pkg/ceremony` — see [../ceremony/](../ceremony/) | go-qrdb |
| CRL, OCSP, HSM-backed CA, profiles, CT | — | **go-xkms**, injected via `WithCertProvider` |

`ceremony.CeremonyCA` is satisfied by both `cert.CertProvider` and go-xkms's
`ca.TrustedCA` through thin adapters, which is what lets the ceremony run
against either.

## Trust boundaries

- **The CA private key is barrier-encrypted at rest** and lives in protected
  memory while unsealed. Sealing destroys it; every mutating operation then
  returns `BarrierSealedError`.
- **`getCAMaterial()` returns a copy** to prevent aliasing the cached key.
- **Client private key DER is wiped immediately after PEM encoding.**
- **`CACertPEM()` needs no barrier check** — the CA certificate is public data
  and is deliberately still served while sealed.
- Issuance, revocation and listing are separately permissioned
  (`certs:generate`, `certs:revoke`, `certs:list`).

## What this feature deliberately does not do

- **No CRL generation and no OCSP.** Revocation is an internal blocklist. A
  deployment whose certificates are checked by external relying parties needs
  the go-xkms provider.
- **No ACME.** That is go-xkms.
- **No server certificate issuance as a product surface** — the daemon's own TLS
  material is a bootstrap concern, see [../firstboot/](../firstboot/).
- **No certificate renewal workflow.** Issue a new one and revoke the old.

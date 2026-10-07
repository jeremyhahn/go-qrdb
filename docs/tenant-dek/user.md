# Tenant DEK Keyring — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

Encrypt your application's data under your tenant's own key without ever
holding the key.

## Encrypt and decrypt

```go
dekc := client.DEK()
ctx = sdk.ContextWithTenant(ctx, "acme") // in process; over a network the
                                         // tenant comes from your credentials

aad := []byte("acme/db/password/v3") // bind the ciphertext to where it lives
enc, err := dekc.TenantDEKEncrypt(ctx, &transport.TenantDEKEncryptRequest{
	Plaintext: secret,
	AAD:       aad,
})
// store enc.Ciphertext; it is opaque and carries its key version

dec, err := dekc.TenantDEKDecrypt(ctx, &transport.TenantDEKDecryptRequest{
	Ciphertext: enc.Ciphertext,
	AAD:        aad,
})
```

**Choose AAD deliberately.** It is not stored in the ciphertext; you present
it again on every decrypt and rewrap. Bind what must not change without a
re-encryption — typically the tenant, the record's path and its version — so a
ciphertext copied to another record fails to decrypt.

## Rotate

```go
st, err := dekc.TenantDEKRotate(ctx, &transport.TenantDEKRequest{})
```

New encryptions use the new version at once, on every node. Everything
encrypted before still decrypts.

## Retire an old version

Retiring destroys a version's key permanently. qrdb cannot know where your
ciphertexts are, so the procedure is yours:

1. Rotate.
2. Rewrap every stored ciphertext still under the old version:
   ```go
   rw, err := dekc.TenantDEKRewrap(ctx, &transport.TenantDEKRewrapRequest{
   	Ciphertext: stored, AAD: aad,
   })
   // write rw.Ciphertext back; rw.FromVersion tells you what it was under
   ```
   Rewrap never returns the plaintext. A ciphertext already under the current
   version comes back unchanged.
3. Retire the old version:
   ```go
   st, err := dekc.TenantDEKRetire(ctx, &transport.TenantDEKRetireRequest{Version: 1})
   ```

The current version cannot be retired. A ciphertext under a retired version
answers `ErrVersionRetired` (409 / `FAILED_PRECONDITION`) from then on.

## What to expect

- **Your tenant only.** Naming another tenant is refused, even for a
  system-tenant administrator.
- **A sealed tenant is unavailable.** While your tenant's barrier is sealed,
  every call answers 503 / `UNAVAILABLE`; unseal and retry.
- **Barrier protection `none`.** The keyring's stored record is kept
  without at-rest encryption, exactly as the rest of a `none` tenant's
  data. Use a keyed barrier strategy if the record must be protected at
  rest. This is independent of key wrapping, below: even under `none`,
  every version's own key is still wrapped under a KEK, never plaintext.
- **Every version's key is wrapped under a key-encrypting key (KEK) —
  there is no unwrapped option.** The operator configures the built-in
  software provider's key directory (`qrdbd --dek-key-dir`) or a
  go-xkms-registered hardware/cloud-KMS provider; without either, encrypt
  and rotate are refused. See
  [key-wrapping/user.md](../key-wrapping/user.md).
- **Deleting the tenant destroys the keyring.** Its ciphertexts become
  permanently undecryptable.

## Permissions

`dek:encrypt` and `dek:decrypt` for applications (built-in User and Operator
roles); `dek:rotate`, `dek:rewrap`, `dek:retire` and `dek:status` for key
administration (built-in Admin role). See [engineering.md](engineering.md#permissions).

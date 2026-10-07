# Tenant DEK Keyring — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column. QUIC frames carry
the same protobuf messages.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Go SDK (`Client.DEK()`) | Permission |
|---|---|---|---|---|---|---|
| Status | `GET /v1/dek/status` | `TenantDEKStatus` | `dek.Status` | `qrdb_dek_status` | `TenantDEKStatus` | `dek:status` |
| Rotate | `POST /v1/dek/rotate` | `TenantDEKRotate` | `dek.Rotate` | `qrdb_dek_rotate` | `TenantDEKRotate` | `dek:rotate` |
| Retire | `POST /v1/dek/retire` | `TenantDEKRetire` | `dek.Retire` | `qrdb_dek_retire` | `TenantDEKRetire` | `dek:retire` |
| Encrypt | `POST /v1/dek/encrypt` | `TenantDEKEncrypt` | `dek.Encrypt` | `qrdb_dek_encrypt` | `TenantDEKEncrypt` | `dek:encrypt` |
| Decrypt | `POST /v1/dek/decrypt` | `TenantDEKDecrypt` | `dek.Decrypt` | `qrdb_dek_decrypt` | `TenantDEKDecrypt` | `dek:decrypt` |
| Rewrap | `POST /v1/dek/rewrap` | `TenantDEKRewrap` | `dek.Rewrap` | `qrdb_dek_rewrap` | `TenantDEKRewrap` | `dek:rewrap` |

Six capabilities on all six transports. Every handler routes through the same
`service.DEKServicer`, so tenant scoping and behaviour cannot diverge.

## Tenant

Every request carries an optional `tenant_id` (REST status takes it as a query
parameter). It may only name the caller's own tenant; empty means the caller's
tenant. Anything else is 403 / `PermissionDenied`, from the system tenant too.
In process, bind the tenant with `sdk.ContextWithTenant`; an unbound context
acts on the system tenant.

## Schemas

Byte fields are base64 (RFC 4648) in JSON and raw `bytes` in protobuf.

Encrypt:

```json
// request
{"tenant_id": "acme", "plaintext": "czNjcmV0", "aad": "YWNtZS9kYi9wYXNzd29yZC92Mw=="}
// response
{"ciphertext": "UURFSwEAAAAAAAAAAg...", "version": 2}
```

Decrypt:

```json
// request
{"ciphertext": "UURFSwEAAAAAAAAAAg...", "aad": "YWNtZS9kYi9wYXNzd29yZC92Mw=="}
// response
{"plaintext": "czNjcmV0", "version": 2}
```

Rewrap:

```json
// request
{"ciphertext": "UURFSwEAAAAAAAAAAQ...", "aad": "..."}
// response — from == to when the input was already current (returned unchanged)
{"ciphertext": "UURFSwEAAAAAAAAAAg...", "from_version": 1, "to_version": 2}
```

Rotate (`{"tenant_id": ""}`), Retire (`{"version": 1}`) and Status all answer
the status shape:

```json
{"tenant_id": "acme", "algorithm": "AES-256-GCM", "current_version": 2,
 "versions": [
   {"version": 1, "created_at": "2026-10-01T12:00:00Z", "retired_at": "2026-10-01T13:00:00Z"},
   {"version": 2, "created_at": "2026-10-01T12:30:00Z", "provider_id": "software", "key_ref": "acme"}]}
```

`current_version` 0 with an empty `versions` list means the tenant has no
keyring yet. In protobuf, times are `created_at_unix` / `retired_at_unix`
seconds; `retired_at_unix` is 0 for a live version. `provider_id` and
`key_ref` name the key-wrap KEK that wrapped each version's key, for
audit — never the key itself; both are empty for a retired version (its
wrapped key, provider ID and key reference are destroyed on retirement).
See [key-wrapping](../key-wrapping/architecture.md).

## Errors

| Condition | REST | gRPC / QUIC / MCP |
|---|---|---|
| Missing or malformed argument | 400 | `INVALID_ARGUMENT` |
| Another tenant named | 403 | `PERMISSION_DENIED` |
| Not a keyring ciphertext | 400 | `INVALID_ARGUMENT` |
| Ciphertext does not authenticate (tampered, different AAD, other tenant) | 400 | `INVALID_ARGUMENT` |
| Version never existed | 404 | `NOT_FOUND` |
| Version retired | 409 | `FAILED_PRECONDITION` |
| Retire the current version | 409 | `FAILED_PRECONDITION` |
| Tenant sealed or keyring store unavailable | 503 | `UNAVAILABLE` |
| Stored keyring corrupt | 500 | `DATA_LOSS` |
| Lost to concurrent updates | 409 | `ABORTED` |
| A legacy-format (pre-key-wrap) record was found | 500 | `DATA_LOSS` |
| No key directory configured for the built-in "software" key-wrap provider | 404 (route absent) | `UNIMPLEMENTED` |
| An unregistered key-wrap provider ID, a bad key reference, or a KEK authentication failure | 400 | `INVALID_ARGUMENT` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Service not configured | 404 (route absent) | `UNIMPLEMENTED` |

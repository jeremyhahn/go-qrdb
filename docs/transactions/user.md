# Transactions and Batch — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19
> Conflicts with this document are defects. Fix them here first.

## Prerequisites

A reachable server, client certificates, and an unsealed barrier — the same
starting point as [../key-value/user.md](../key-value/user.md). You also need
`transaction:execute` for `tx` and `batch:execute` for `batch`.

## Which one do you want?

- You want to write several keys **only if** something is still true → **`tx`**.
- You want to write several keys **together, no condition** → **`batch`**.
- You have a CSV or JSON file of records to load → **`batch import`**.

## First run

Both `tx` and `batch` read their request from a JSON file. Keys and values are
byte strings on the wire, so they are base64 in JSON.

```bash
printf 'config/lock' | base64      # Y29uZmlnL2xvY2s=
printf 'free'        | base64      # ZnJlZQ==
```

A "claim this lock only if it is free" transaction:

```bash
cat > claim.json <<'JSON'
{
  "compares":    [ { "key": "Y29uZmlnL2xvY2s=", "target": 3, "result": 0, "value": "ZnJlZQ==" } ],
  "success_ops": [ { "type": 0, "key": "Y29uZmlnL2xvY2s=", "value": "aGVsZA==" } ],
  "failure_ops": [ { "type": 1, "key": "Y29uZmlnL2xvY2s=" } ]
}
JSON
qrdb tx execute --file claim.json --output json
```

Read `succeeded` in the response. `true` means your compares held and the
success ops ran. `false` means someone got there first — the command still
exits 0, because that is an answer, not a failure.

## Common tasks

**Write several keys atomically, no condition.**

```bash
echo '{"ops":[
  {"type":0,"key":"YS9i","value":"MQ=="},
  {"type":0,"key":"YS9j","value":"Mg=="}
]}' | qrdb batch execute --file -
```

The response carries `revision`: the one MVCC revision every write in the batch
committed at, which is the `mod_revision` a later read of any of those keys
reports. A batch with no put or delete commits nothing and answers `0`. Every
transport returns it — `revision` in the REST JSON, gRPC, QUIC and Unix
responses, `Revision` on the embedded client's `BatchResponse`, and on MCP
`qrdb_batch_execute` answers `{"Revision": N, "Responses": [...]}`, an object
whose `Responses` holds the per-op results.

**Load a CSV.** Pick which column is the key; by default the whole row is stored
as JSON under it.

```bash
qrdb batch import --file users.csv --key-field id --skip-header
```

**Store one column as the value instead of the whole row.**

```bash
qrdb batch import --file users.csv --key-field id \
     --value-mode field --value-field payload --skip-header
```

**Load into a specific database.**

```bash
qrdb --database billing batch import --file invoices.csv --key-field invoice_id
```

**Refuse a partial load.** With `--transactional`, one bad row aborts the whole
import rather than leaving you with a half-loaded dataset.

```bash
qrdb batch import --file ledger.ndjson --format ndjson \
     --key-field entry_id --transactional
```

**Pipe from another tool.**

```bash
jq -c '.[]' export.json | qrdb batch import --file - --format ndjson --key-field id
```

## Troubleshooting

**`succeeded: false` and nothing changed.** That is the compare failing. Read
back the key you compared on, decide whether to retry, and re-run. This is the
normal way a compare-and-swap loop works.

**"format could not be inferred".** `auto` looks at the extension and the first
8 KB. Piping from stdin with no extension often defeats it — pass `--format`
explicitly.

**A large import is slow or the connection drops partway through.**
`batch import` sends your file as many small chunks rather than one big
message (no transport accepts a message larger than
`transport.max_message_bytes`, 4 MiB by default), so a multi-gigabyte file
is normal, not a misuse — but every chunk still needs a round trip. If the
connection drops mid-upload, re-run the same `batch import` command; the
session that was in progress expires on its own (the lease TTL,
`import_session.session_ttl_seconds`, 1 hour by default) and the retry
opens a fresh one.

**Rows imported but the keys look wrong.** Check `--key-field`: it names a field
*in the record*, not a key prefix. With `--value-mode row` the entire row is
stored as JSON under that field's value.

**You wanted the records in a collection, not in KV.** There is no
collection-targeted import. Collections take one document at a time via
`qrdb collection doc insert` — see [../collections/user.md](../collections/user.md).

**Some rows failed and you cannot tell which.** Use `--output json`; the
`errors` array names the row number, the key, and the message for each failure.

# Transactions and Batch — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

Command groups: `qrdb tx` and `qrdb batch`. Both accept the root persistent
flags documented in [../key-value/cli.md](../key-value/cli.md#global-flags).

## `qrdb tx execute`

Execute a transaction from a JSON file.

| Flag | Type | Default | Required |
|---|---|---|---|
| `--file` | string | — | yes — path, or `-` for stdin |

The file is a `TransactionRequest`: `compares`, `success_ops`, `failure_ops`.
`target` and `result` are integer enums (`target`: 0 version, 1 create
revision, 2 mod revision, 3 value; `result`: 0 equal, 1 greater, 2 less,
3 not-equal). `type` on an op is 0 put or 2 delete; 1 get and 3 range are
refused (400 / `INVALID_ARGUMENT`), because a branch only writes.

```bash
cat > txn.json <<'JSON'
{
  "compares": [
    { "key": "Y29uZmlnL2xvY2s=", "target": 3, "result": 0, "value": "ZnJlZQ==" }
  ],
  "success_ops": [
    { "type": 0, "key": "Y29uZmlnL2xvY2s=", "value": "aGVsZA==" }
  ],
  "failure_ops": [
    { "type": 2, "key": "Y29uZmlnL2xvY2s=" }
  ]
}
JSON
qrdb tx execute --file txn.json
qrdb tx execute --file - < txn.json
```

Keys and values are base64 in JSON because they are `[]byte` on the wire.

Output carries `succeeded`, `revision` and the positional `responses`. Exit
status is zero whether or not the compares held — a failed compare is a result,
not an error.

## `qrdb batch execute`

Execute an unconditional, atomic list of operations from a JSON file.

| Flag | Type | Default | Required |
|---|---|---|---|
| `--file` | string | — | yes — path, or `-` for stdin |

```bash
echo '{"ops":[{"type":0,"key":"YQ==","value":"MQ=="},{"type":2,"key":"Yg=="}]}' \
  | qrdb batch execute --file -
```

JSON output (the default) carries `revision`, the single MVCC revision every
op in the batch committed at (`0` for a batch with no put or delete), and the
positional `responses`. `--output table` prints only the per-op responses.

## `qrdb batch import`

Import key-value records from a CSV, JSON, NDJSON, YAML or archive file into the
KV store.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--file` | string | — | path to import, `-` for stdin |
| `--format` | string | `auto` | `auto \| csv \| json \| ndjson \| yaml \| archive` |
| `--key-field` | string | — | field to extract as the record key |
| `--value-mode` | string | `row` | `field` uses `--value-field`; `row` serializes the whole row as JSON |
| `--value-field` | string | — | field name when `--value-mode=field` |
| `--skip-header` | bool | false | skip the first row (CSV only) |
| `--transactional` | bool | false | all-or-nothing: any row error rolls back the entire import |

`auto` infers the format from the file extension and the first 8 KB of content.

Imports always land in the raw KV keyspace — there is no collection-targeted
import. Use the global `--database` flag to scope the import to a named
database; `/batch` is a data-plane endpoint, so the globally scoped connection
already carries that database context on every request.

**There is no file-size limit of its own.** `qrdb batch import` drives a
chunked import session underneath (`ImportBegin`, as many `ImportAppend`
calls as the file needs, `ImportCommit`) — the file is split into pieces no
larger than the transport's pre-auth message cap
(`transport.max_message_bytes`, default 4 MiB) before any of it is sent,
identically on every `--transport`.

```bash
qrdb batch import --file data.csv --key-field id --value-mode row
qrdb batch import --file - --format ndjson --key-field id \
     --value-mode field --value-field payload
qrdb --database billing batch import --file invoices.csv \
     --key-field invoice_id --skip-header --transactional
```

## `qrdb batch import-formats`

List the supported import file formats. No flags.

```bash
qrdb batch import-formats
```

## Parity

`qrdb tx execute` is driven on every network transport by `tx_cli_test.go`
(`make integration-test-transactions`); the `qrdb batch` verbs are driven by
`batch_cli_test.go` and `batch_import_cli_test.go`, which
`test/integration/features.mk` selects under `make integration-test-key-value`.
Per-transport accounting: the `transactions` row of
[`test/integration/COVERAGE.md`](../../test/integration/COVERAGE.md).

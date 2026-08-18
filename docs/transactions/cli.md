# Transactions and Batch — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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
3 not-equal). `type` on an op is 0 put, 1 get, 2 delete, 3 range.

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
    { "type": 1, "key": "Y29uZmlnL2xvY2s=" }
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

**Maximum import size depends on `--transport`**: REST, gRPC, Unix and MCP
accept up to 100 MB; QUIC accepts roughly 63 MB, bounded by the QUIC wire
protocol's shared per-RPC payload cap rather than an import-specific limit.

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

Every verb here has a `CommandDefinition` in the integration parity suite.

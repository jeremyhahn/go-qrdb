# Secondary Indexes — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Command group: `qrdb index` — create, drop, query, scan and list secondary
indexes. All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags), including `--database`.

Indexes declared with struct tags on an entity type are registered
automatically at DAO construction and do **not** need `index create`. This
command group is for indexes on data with no Go entity type, and for
inspection.

## `qrdb index create <index-name>`

Create a new secondary index.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--field` | string | — | field name to index |
| `--unique` | bool | false | enforce a uniqueness constraint |
| `--sparse` | bool | false | allow missing indexed field values |

The index name is database-scoped: `{database}.{entityType}.{FieldName}`.

```bash
qrdb index create qrdb.User.Email --field Email --unique
qrdb index create qrdb.Order.Status --field Status
qrdb index create qrdb.User.Nickname --field Nickname --sparse
```

## `qrdb index drop <index-name>`

Remove a secondary index. No flags.

```bash
qrdb index drop qrdb.Order.Status
```

## `qrdb index query <index-name>`

Query an index for exact value matches. Returns the matching entity keys.

| Flag | Type | Required |
|---|---|---|
| `--value` | string | yes — exact value to search for |

```bash
qrdb index query qrdb.User.Email --value alice@example.com
qrdb index query qrdb.Order.Status --value shipped --output json
```

## `qrdb index scan <index-name>`

Scan an index within a value range. Returns entries of `(value, keys)`.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--start` | string | unbounded | start of value range, inclusive |
| `--end` | string | unbounded | end of value range, exclusive |

```bash
qrdb index scan qrdb.Order.Total --start 100 --end 500
qrdb index scan qrdb.User.Email --start a --end b
```

The range is half-open `[start, end)`. Ordering follows the index's encoding,
which is order-preserving for both strings and numerics.

## `qrdb index list`

List all secondary indexes in scope. No flags.

```bash
qrdb index list
qrdb --database billing index list
```

## Notes

- Querying a name that is not an index is an error, not a silent full scan.
- Creating a unique index over data that already violates uniqueness fails.
- Compound indexes are declared in Go (`CompoundIndexRegisterer`) and have no
  CLI creation verb; `index list` shows them.

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite.

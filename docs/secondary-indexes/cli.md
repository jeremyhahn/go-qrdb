# Secondary Indexes — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-27
> Conflicts with this document are defects. Fix them here first.

Command group: `qrdb index` — create, drop, query, scan and list secondary
indexes. All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags), including `--database`.

Indexes declared with struct tags on an entity type are registered
automatically at DAO construction and do **not** need `index create`. This
command group is for indexes on data with no Go entity type, and for
inspection.

## Exit codes

Every verb below exits `0` on success and `1` on any error, which it prints to
stderr.

## `qrdb index create <index-name>`

Create a new secondary index.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--field` | string | — | field name to index; required |
| `--unique` | bool | false | enforce a uniqueness constraint |
| `--sparse` | bool | false | allow missing indexed field values |
| `--numeric` | bool | false | index values as numbers, so scans run in numeric order |

The index name is database-scoped: `{database}.{entityType}.{FieldName}`.

```bash
qrdb index create qrdb.User.Email --field Email --unique
qrdb index create qrdb.Order.Status --field Status
qrdb index create qrdb.User.Nickname --field Nickname --sparse
qrdb index create qrdb.Order.Total --field Total --numeric
```

Without `--numeric` an index compares its values as text, so `"10"` sorts
before `"2"`. With it, values are compared as numbers — integers and floats
in one order, `-0` equal to `0` — and a stored value that is not a number,
or is `NaN`, is not indexed. See
[architecture.md](architecture.md#numeric-indexes-and-range-plans) for the
full order.

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

The range is half-open `[start, end)`. Ordering follows the index's encoding:
byte order for a text index, numeric order for a `--numeric` one. On a
numeric index each bound must be a number (`2`, `-1.5`, `6.02e23`, `Inf`);
`NaN` or a non-number is refused. Values print in canonical form, so a
stored `1.50` is listed as `1.5`.

```bash
qrdb index scan qrdb.Order.Total --start 2          # every total >= 2, 10 included
qrdb index scan qrdb.Order.Total --start -5 --end 0 # negatives from -5
```

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

Every verb here is driven on every network transport by `index_cli_test.go`
(`make integration-test-secondary-indexes`). Per-transport accounting: the
`secondary-indexes` row of
[`test/integration/COVERAGE.md`](../../test/integration/COVERAGE.md).

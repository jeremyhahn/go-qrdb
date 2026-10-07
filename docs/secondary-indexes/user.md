# Secondary Indexes — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

An index turns "find every user whose email is X" from a full scan into a
lookup. Most of the time you do not create one by hand: you tag the field on
your Go type and the DAO registers the index for you.

## Prerequisites

A reachable server, client certificates, an unsealed barrier, and the `index:*`
permissions for the verbs you use.

## The usual path: tag the field

```go
type User struct {
	ID       uint64 `json:"id"`
	Email    string `json:"email"    index:"unique"`
	Status   string `json:"status"   index:"true"`
	Username string `json:"username" index:"unique,case_insensitive"`
	Name     string `json:"name"`
}
```

Build a DAO for `User` and the indexes exist. No `index create` call, no
migration step. Then query by intent and let the planner pick the access path:

```go
users, err := dao.Find(ctx).Where("Email = ?", "alice@example.com").Execute()
```

## Creating an index by hand

Use the CLI when the data has no Go entity type behind it.

```bash
qrdb index create qrdb.User.Email --field Email --unique
```

Index names are `{database}.{entityType}.{FieldName}` — the scoping is what lets
`myapp.User.Email` and `qrdb.User.Email` coexist.

## Common tasks

**Look up entities by an exact value.**

```bash
qrdb index query qrdb.User.Email --value alice@example.com
```

**Scan a value range.** The range is `[start, end)` — start included, end
excluded.

```bash
qrdb index scan qrdb.Order.Total --start 100 --end 500
```

**See what indexes exist.**

```bash
qrdb index list
qrdb --database billing index list
```

**Drop one you no longer need.** Each index costs a write on every entity
change, so unused indexes are not free.

```bash
qrdb index drop qrdb.Order.Status
```

**Index a field that most records do not have.** Use `--sparse`, so records
missing the field are left out rather than all bunched under an empty value.

```bash
qrdb index create qrdb.User.Nickname --field Nickname --sparse
```

**Speed up a query that filters on two fields.** Declare a compound index on the
entity type. Field order matters — put the field you always filter on first.

```go
func (m Message) CompoundIndexes() []index.CompoundIndexDef {
	return []index.CompoundIndexDef{
		{Fields: []string{"ConversationID", "Timestamp"}},
	}
}
```

Now `ConversationID = ? AND Timestamp > ?` uses one index instead of one index
plus a post-filter.

## Troubleshooting

**A compound index is not being used.** Check the leading field. An index on
`(A, B)` helps `A = ?` and `A = ? AND B = ?`, but does nothing for `B = ?` on
its own — the query must match a contiguous prefix starting at the first field.

**`A = ? AND C = ?` is still slow with an index on `(A, B, C)`.** There is a gap
at `B`, so only the `A` prefix is used and `C` is filtered afterwards. Either
add an index on `(A, C)` or accept the post-filter.

**Creating a unique index failed.** The existing data already violates
uniqueness. Find and fix the duplicates first — the index refuses to be created
over data it could not enforce.

**A write was rejected with a uniqueness error.** That is the unique index doing
its job. It refuses the write rather than overwriting the other record.

**A query against an index name returns "not found".** The name is wrong — most
often the database or entity prefix is missing. It is a hard error rather than a
silent full scan on purpose, so a typo shows up immediately instead of as a
mysterious slowdown in production.

**Writes got slower after adding indexes.** Expected: every index adds one
storage write per entity `Put` or `Delete`. Faster reads are bought with slower
writes. Drop the indexes you are not querying.

**You want `WHERE LOWER(name) = ?` or an index over only active rows.**
Expression indexes and partial indexes do not exist. Use a
`case_insensitive` tag for the first case; for the second, index the status
field and let the planner combine.

**You want text search, not exact match.** Indexes do exact and range matching.
Free-text search is a separate feature — see
[../full-text-search/user.md](../full-text-search/user.md).

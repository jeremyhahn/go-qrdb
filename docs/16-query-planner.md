# Query Planner & Fluent Query API

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-13
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

## 1. Overview

QRDB includes an internal query planner that automatically selects optimal execution strategies based on available indexes and query predicates. The planner is not exposed directly to callers; instead, applications use the Data Access Object (DAO) layer which provides a GORM-style fluent query API.

The planner evaluates the cost of different execution strategies (full scan, index lookup, index range scan) and selects the cheapest one, while respecting the semantics and ordering guarantees required by the query.

## 2. Architecture

The query planner lives in `pkg/planner/` and operates at the service layer, between DAO query builders and the underlying MVCC state machine.

```
Application
  |
  v
DAO Layer (Fluent API)
  | Find().Where().OrderBy().Limit()
  v
Query Builder
  | Parse predicates, validate fields/operators
  v
Query Planner (pkg/planner/)
  | Inspect available indexes
  | Cost each execution strategy
  | Select optimal plan
  v
Execution Engine
  | Execute IndexLookup / IndexScan / FullScan
  | Post-filter remaining predicates
  | Apply ordering and pagination
  v
Results
```

The `pkg/planner/` package contains:

- **Planner** -- Cost-based query optimizer
- **Predicate** -- Query filter representation (field, operator, value)
- **Plan** -- The chosen strategy (`PlanType`: `PlanFullScan`, `PlanIndexLookup`, `PlanIndexScan`, `PlanCompoundIndexLookup`, `PlanCompoundIndexScan`, `PlanIndexIntersection`) plus the post-filters
- **Cost estimates** (`pkg/planner/cost.go`) -- Hardcoded constants per strategy

## 3. Cost Model

The planner assigns costs to different execution strategies to guide plan selection:

| Strategy | Cost | Reason |
|----------|------|--------|
| Unique index lookup (=) | 1 | O(1) hash lookup |
| Compound index exact match | 3 | One lookup over several fields |
| Normal index lookup (key match) | 10 | O(k) where k is cardinality |
| Index intersection | 15 added | Overhead of intersecting two index results |
| Compound index prefix match | 20 | Bounded scan over a compound prefix |
| Index range scan (>, >=, <, <=, BETWEEN) | 50 | O(log n + k) with range iteration |
| Full table scan | 1000 | O(n) linear scan |

Each predicate left to a post-filter adds a penalty of 20. The planner
combines costs additively. For example:

- Query: `WHERE Status = 'active' AND Age > 30`
- If `Status` has a unique index: Cost = 1 (unique lookup) + 20 (post-filter Age > 30)
- If only `Age` is indexed: Cost = 50 (range scan Age > 30) + 20 (post-filter Status)
- If neither is indexed: Cost = 1000 (full scan)

The planner selects the lowest-cost plan.

## 4. DAO Fluent API

The DAO layer provides a GORM-style query builder interface. This is the
**recommended** way to query entities. You do not name indexes, pick
strategies, or hand-write lookups — the planner does that automatically
based on struct tags on your entity.

### Defining an indexed entity

Indexes are declared with `index` struct tags on the entity. At DAO
construction time they are parsed into the planner's catalog.

```go
type User struct {
    ID        uint64 `json:"id"`
    Email     string `json:"email"    index:"unique"`
    Status    string `json:"status"   index:"true"`
    Age       int    `json:"age"      index:"true"`
    Name      string `json:"name"`    // not indexed
    CreatedAt int64  `json:"createdAt"`
}

func (u *User) EntityID() uint64      { return u.ID }
func (u *User) SetEntityID(id uint64) { u.ID = id }
```

Supported tag values: `index:"true"` (normal), `index:"unique"`,
`index:"case_insensitive"`, or a comma-separated combination
(e.g. `index:"unique,case_insensitive"`). Compound indexes are declared
by implementing the `index.CompoundIndexRegisterer` interface — see
[08-secondary-indexes.md](08-secondary-indexes.md).

### QueryBuilder signature

```go
// Terminal methods execute the query; builder methods chain.
func (d *genericDAO[E]) Find(ctx context.Context) *QueryBuilder[E]

func (q *QueryBuilder[E]) Where(expr string, args ...interface{}) *QueryBuilder[E]
func (q *QueryBuilder[E]) OrderBy(field string, dir ...SortDirection) *QueryBuilder[E]
func (q *QueryBuilder[E]) Limit(n int) *QueryBuilder[E]
func (q *QueryBuilder[E]) Page(page, pageSize int) *QueryBuilder[E]

// Terminals — context is captured at Find(ctx), not re-passed here.
func (q *QueryBuilder[E]) Execute() ([]E, error)
func (q *QueryBuilder[E]) ExecutePage() (PageResult[E], error)
func (q *QueryBuilder[E]) First() (E, error)
func (q *QueryBuilder[E]) Count() (int, error)
```

`SortDirection` is an int alias with constants `dao.SortAscending`
(default) and `dao.SortDescending`. Direction is **not** a string.

### Query Examples

**Exact match (uses unique index if available):**

```go
user, err := userDAO.Find(ctx).
    Where("Email = ?", "alice@example.com").
    First()
// Planner selects: IndexLookup on unique Email index (cost 1)
```

**Range query (uses index scan if indexed):**

```go
users, err := userDAO.Find(ctx).
    Where("Age > ?", 30).
    OrderBy("Name").
    Execute()
// Planner selects: IndexScan on Age index (cost 50) + post-filter + sort
```

**Compound query (picks best index, post-filters rest):**

```go
users, err := userDAO.Find(ctx).
    Where("Status = ?", "active").
    Where("Age >= ?", 18).
    OrderBy("Name").
    Limit(100).
    Execute()
// Planner evaluates:
// - Use Status index: cost 10 + post-filter Age >= 18
// - Use Age index: cost 50 + post-filter Status
// - Full scan: cost 1000
// Selects lowest cost plan
```

**BETWEEN query:**

```go
products, err := productDAO.Find(ctx).
    Where("Price BETWEEN ? AND ?", 10.0, 100.0).
    Execute()
// Planner selects: IndexScan on Price index (cost 50) if indexed, else FullScan
```

**IN operator (multi-value lookup):**

```go
users, err := userDAO.Find(ctx).
    Where("Role IN ?", []string{"admin", "moderator"}).
    Execute()
// Planner selects: Multiple index lookups (cost 10 * cardinality) if indexed
```

**Prefix matching:**

```go
users, err := userDAO.Find(ctx).
    Where("Username PREFIX ?", "alice").
    Execute()
// Planner selects: Prefix scan on Username index (cost 50) if indexed
```

**Pagination:**

```go
page, err := userDAO.Find(ctx).
    Where("Status = ?", "active").
    OrderBy("CreatedAt", dao.SortDescending).
    Page(2, 20).  // Page 2, 20 items per page
    ExecutePage()
// Returns: PageResult{Entities, Total, Page, PageSize, HasMore}
```

**Count only:**

```go
n, err := userDAO.Find(ctx).
    Where("Status = ?", "active").
    Count()
```

## 5. Supported Operators

The query planner supports nine comparison operators:

| Operator | Syntax | Example | Index Strategy |
|----------|--------|---------|-----------------|
| Equality | `=` | `Age = 30` | IndexLookup (cost 10) |
| Inequality | `!=` | `Status != "inactive"` | FullScan + post-filter |
| Greater than | `>` | `Price > 100` | IndexScan (cost 50) |
| Greater or equal | `>=` | `CreatedAt >= 2024-01-01` | IndexScan (cost 50) |
| Less than | `<` | `Stock < 10` | IndexScan (cost 50) |
| Less or equal | `<=` | `Score <= 100` | IndexScan (cost 50) |
| IN (multi-value) | `IN` | `Role IN (admin, user)` | Multiple IndexLookups |
| BETWEEN (range) | `BETWEEN` | `Age BETWEEN 18 AND 65` | IndexScan (cost 50) |
| Prefix matching | `PREFIX` | `Name PREFIX alice` | IndexScan (cost 50) |

Only these nine operators are accepted. Unrecognized operators result in a validation error at query build time.

## 6. Security Model

The query planner enforces multiple layers of security to prevent injection and ensure safe execution:

### Field Validation

Field names are validated against the entity struct fields in O(1) time using a field map:

```go
// At index creation time:
fieldMap := make(map[string]bool)
for _, field := range entity.Fields() {
    fieldMap[field.Name] = true
}

// At query time:
if !fieldMap[queryField] {
    return fmt.Errorf("field not found: %s", queryField)
}
```

Only fields defined in the entity struct can be queried. This prevents accidental or malicious references to internal fields.

### Operator Whitelist

The planner maintains a fixed set of nine allowed operators. Unrecognized operators are rejected during query parsing:

```go
validOps := map[string]bool{
    "=": true, "!=": true,
    ">": true, ">=": true, "<": true, "<=": true,
    "IN": true, "BETWEEN": true, "PREFIX": true,
}

if !validOps[operator] {
    return fmt.Errorf("unsupported operator: %s", operator)
}
```

### Parameterized Values

All user-supplied values are passed via `?` placeholders and never string-interpolated into queries. This prevents SQL injection:

```go
// Safe: values bound separately
.Where("Email = ?", userInput)

// Unsafe (prevented by API design):
.Where("Email = '" + userInput + "'")  // Would panic if attempted
```

### Zero-Allocation Expression Parser

The predicate parser is designed to minimize allocations and avoid reflection during hot-path execution:

```go
// Predicate structure (copied, not allocated):
type Predicate struct {
    Field     string        // 16 bytes
    Operator  string        // 16 bytes
    Value     interface{}   // 16 bytes
    // Total: 48 bytes on stack
}
```

Parsing results are checked once at query build time, before execution begins.

## 7. Execution Flow

The execution flow from application code through to results is:

```
1. Application calls: userDAO.Find(ctx)
   Result: Empty QueryBuilder

2. Application calls: .Where("Status = ?", "active")
   Action: Parse predicate, validate field/operator, store filter
   Result: QueryBuilder with pending filters

3. Application calls: .OrderBy("Name").Limit(100)
   Action: Store ordering requirement, store limit
   Result: QueryBuilder with predicates, ordering, limit

4. Application calls: .Execute()
   Action: Invoke planner.Plan(predicates)

5. Planner inspects indexes:
   - Does Status have an index? (Cost 10)
   - Is Name indexed? (Used for ordering)
   - Can we satisfy ordering from index scan?

6. Planner selects optimal Plan:
   - Primary strategy: IndexLookup on Status (cost 10, non-unique index)
   - Post-filters: None (Status was indexed, ordering from result order)
   - Limit: 100 items

7. Execution engine executes plan:
   a. IndexLookup("Status", "active") → candidate set
   b. Sort by Name if needed (usually free from index order)
   c. Apply limit (100 items)
   d. Return results

8. Application receives: []User with 100 active users, sorted by name
```

The planner **always** ensures:

- **Correctness**: All predicates are evaluated before returning results
- **Completeness**: Results contain all matching records (within limit)
- **Ordering**: If OrderBy is specified, results are sorted correctly
- **Pagination**: Offset and limit are applied after filtering and sorting

## 8. Query Plan Caching

The planner does not cache plans. Every query is planned when it runs; planning
is a pass over the entity's declared indexes and costs a handful of constant
comparisons.

## 9. Power-User Escape Hatches

`Find().Where()` is the recommended path for virtually all application
code. For callers who need explicit index control, the DAO also exposes
low-level methods that bypass the planner and target a specific index by
name:

```go
// Exact-value lookup on a named index:
results, err := userDAO.QueryByIndex(ctx, "Email", "alice@example.com")

// Half-open range scan on a named index:
results, err := userDAO.ScanByIndex(ctx, "Age", 18, 65)

// Paginated lookup on a named index:
page, err := userDAO.PageByIndex(ctx, "Status", "active",
    dao.PageQuery{Page: 1, PageSize: 25})

// Compound index lookup / scan:
results, err := userDAO.QueryByCompoundIndex(ctx, "User.TenantID_Status",
    []interface{}{"acme", "active"})
results, err := msgDAO.ScanByCompoundIndex(ctx, "Message.ConversationID_Timestamp",
    []interface{}{"conv-1"}, startTs, endTs)
```

These are useful when:

- Pinning to a specific index for reproducibility in benchmarks
- Driving reports where you already know the access pattern
- Bypassing the planner while debugging plan selection

Prefer `Find().Where()` in application code. The explicit methods are
fully supported but require you to know index names and selectivity —
responsibilities the fluent API handles for you.

## 10. Performance Characteristics

### Plan Selection Time

Plan selection cost is sub-millisecond:

| Predicates | Index count | Plan time |
|-----------|-------------|-----------|
| 1 | 10 | <50 µs |
| 3 | 50 | <100 µs |
| 10 | 100 | <300 µs |

### Query Execution Time

Depends on the selected strategy:

| Strategy | Expected Time | Example |
|----------|--------------|---------|
| Unique index lookup | <1 ms | Find user by email |
| Index range scan (100 results) | 1–5 ms | List users in age range |
| Full scan (1M keys, 100 results) | 50–200 ms | Scan without index |

### Memory Usage

Predicate parsing allocates minimal memory:

```
Single predicate: ~80 bytes (struct + field/operator strings)
Query builder state: ~200 bytes (predicates + limit + ordering)
Cached plan: ~500 bytes (execution instructions)
```

## Cross-References

- [Secondary Indexes](08-secondary-indexes.md) -- Index types, creation, and management
- [MVCC Engine](key-value/engineering.md) -- Underlying key-value versioning
- [Configuration](12-configuration.md) -- `index.max_indexes` setting
- [API Reference](api-server/api.md) -- DAO query endpoints

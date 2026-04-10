# Aggregations

QRDB's GenericDAO supports aggregation queries via the `GroupBy` and
`Aggregate` methods on the `QueryBuilder`. These enable computing counts,
sums, averages, minimums, and maximums across entity collections, optionally
grouped by a field.

## Usage

### Count All Matching Entities

```go
result, err := userDAO.Find(ctx).
    Where("IsActive = ?", true).
    Aggregate(dao.Count())

totalActive := result.Groups[0].Values["count"].(int64)
```

### Count Per Group (Signal: Unread Messages Per Conversation)

```go
result, err := messageDAO.Find(ctx).
    Where("IsRead = ?", false).
    GroupBy("ConversationID").
    Aggregate(dao.Count())

for _, group := range result.Groups {
    convID := group.Key.(string)
    unreadCount := group.Values["count"].(int64)
    fmt.Printf("Conversation %s: %d unread\n", convID, unreadCount)
}
```

### Multiple Aggregations

```go
result, err := orderDAO.Find(ctx).
    GroupBy("CustomerID").
    Aggregate(
        dao.Count(),
        dao.Sum("Amount"),
        dao.Avg("Amount"),
        dao.Min("Amount"),
        dao.Max("Amount"),
    )
```

### Without GroupBy

When `GroupBy` is not called, all matching entities are treated as a
single group:

```go
result, err := messageDAO.Find(ctx).
    Aggregate(dao.Count(), dao.Max("Timestamp"))

totalMessages := result.Groups[0].Values["count"].(int64)
latestTimestamp := result.Groups[0].Values["max_Timestamp"].(float64)
```

## API Reference

### AggFunc Constructors

| Function | Alias | Description |
|----------|-------|-------------|
| `Count()` | `"count"` | Count of entities in each group |
| `Sum(field)` | `"sum_{field}"` | Sum of a numeric field |
| `Avg(field)` | `"avg_{field}"` | Average of a numeric field |
| `Min(field)` | `"min_{field}"` | Minimum value of a field |
| `Max(field)` | `"max_{field}"` | Maximum value of a field |

### AggregateResult

```go
type AggregateResult struct {
    Groups []GroupResult
}

type GroupResult struct {
    Key    interface{}            // Group key value (nil if no GroupBy)
    Values map[string]interface{} // Alias -> computed value
}
```

### Value Types in Results

| AggFunc | Result Type |
|---------|-------------|
| `Count()` | `int64` |
| `Sum()` | `float64` |
| `Avg()` | `float64` |
| `Min()` | `float64` |
| `Max()` | `float64` |

## Performance

Aggregations execute as a two-phase operation:

1. **Query phase**: The base query (with Where filters) is executed using
   the planner's optimal index selection. This benefits from compound
   indexes when available.

2. **Aggregation phase**: Matching entities are streamed through in-memory
   accumulators. Each entity is visited once; accumulators maintain running
   state (count, sum, min, max) per group.

Memory usage is proportional to the number of groups, not the number of
entities. For a query like "unread count per conversation" with 10,000
unread messages across 50 conversations, only 50 accumulators are created.

## Limitations

- Aggregation values are always `float64` (except Count which is `int64`).
  Integer precision may be lost for very large values (>2^53).
- GroupBy supports a single field only. Multi-field grouping is not yet
  supported.
- Results are not sorted by default. Apply sorting in application code.
- Aggregations load all matching entities into memory before grouping.
  For very large result sets, consider adding more specific Where filters
  to reduce the scan.

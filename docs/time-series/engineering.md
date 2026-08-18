# Time Series — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

> **Build-tag gated: `qrdb_ts`** (`WITH_QRDB_TS=1`). Without it the CLI verb
> group and transport handlers are compiled out and `pkg/capabilities` does not
> register `timeSeries`.

## Exported surface

`sdk/go`:

```go
func (c *Client) TimeSeries() transport.TimeSeriesClient
```

`sdk/go/transport`:

```go
type TimeSeriesClient interface {
	TSWrite(ctx context.Context, point *TSDataPoint) error
	TSWriteBatch(ctx context.Context, points []TSDataPoint) error
	TSWritePoint(ctx context.Context, point *TSDataPoint) error
	TSQuery(ctx context.Context, req *TSQueryRequest) (*TSQueryResponse, error)
	TSAggregate(ctx context.Context, req *TSAggregateRequest) (*TSAggregateResponse, error)
	TSRichQuery(ctx context.Context, req *TSRichQueryRequest) (*TSRichQueryResponse, error)
	TSDeleteSeries(ctx context.Context, req *TSDeleteRequest) (*TSDeleteResponse, error)
	TSGetRetention(ctx context.Context, req *TSGetRetentionRequest) (*TSGetRetentionResponse, error)
	TSSetRetention(ctx context.Context, req *TSSetRetentionRequest) error
	TSListMetrics(ctx context.Context, req *TSListMetricsRequest) (*TSListMetricsResponse, error)
}
```

### Types

```go
type TSTag struct{ Key, Value string }

type TSDataPoint struct {
	Metric      string  `json:"metric"`
	Value       float64 `json:"value"`
	TimestampNs int64   `json:"timestamp_ns"`
	Tags        []TSTag `json:"tags,omitempty"`
}

type TSQueryRequest struct {
	Metric      string  `json:"metric"`
	StartNs     int64   `json:"start_ns"`
	EndNs       int64   `json:"end_ns"`
	Tags        []TSTag `json:"tags,omitempty"`
	Limit       int32   `json:"limit,omitempty"`
	Descending  bool    `json:"descending,omitempty"`
	Consistency string  `json:"consistency,omitempty"`
}
type TSQueryResponse struct{ Points []TSDataPoint }

type TSAggregateRequest struct {
	Metric      string  `json:"metric"`
	StartNs     int64   `json:"start_ns"`
	EndNs       int64   `json:"end_ns"`
	Function    AggFunc `json:"function"`
	IntervalNs  int64   `json:"interval_ns,omitempty"`
	Consistency string  `json:"consistency,omitempty"`
}
type TSAggResult struct {
	TimestampNs int64   `json:"timestamp_ns"`
	Value       float64 `json:"value"`
	Count       int64   `json:"count"`
}
type TSAggregateResponse struct{ Results []TSAggResult }

type TSRichQueryRequest struct {
	Metric      string   `json:"metric"`
	StartNs     int64    `json:"start_ns"`
	EndNs       int64    `json:"end_ns"`
	Tags        []TSTag  `json:"tags,omitempty"`
	Aggregation AggFunc  `json:"aggregation,omitempty"`
	IntervalNs  int64    `json:"interval_ns,omitempty"`
	GroupBy     []string `json:"group_by,omitempty"`
	Limit       int      `json:"limit,omitempty"`
	Percentile  float64  `json:"percentile,omitempty"`
	Consistency string   `json:"consistency,omitempty"`
}
type TSRichQueryResponse struct {
	Metric          string
	Tags            []TSTag
	Points          []TSDataPoint
	AggregatedValue *float64          `json:"aggregated_value,omitempty"`
	GroupedResults  []TSGroupedResult `json:"grouped_results,omitempty"`
}
type TSGroupedResult struct {
	Tags            []TSTag
	Points          []TSDataPoint
	AggregatedValue *float64
}

type TSDeleteRequest struct {
	Metric  string  `json:"metric"`
	Tags    []TSTag `json:"tags,omitempty"`
	StartNs int64   `json:"start_ns,omitempty"`
	EndNs   int64   `json:"end_ns,omitempty"`
}
type TSDeleteResponse struct{}

type TSRetentionPolicy struct {
	DurationNs   int64   `json:"duration_ns"`
	ResolutionNs int64   `json:"resolution_ns,omitempty"`
	Aggregation  AggFunc `json:"aggregation,omitempty"`
}
type TSGetRetentionRequest  struct{ Metric string }
type TSGetRetentionResponse struct{ Policy *TSRetentionPolicy }
type TSSetRetentionRequest  struct{ Metric string; Policy *TSRetentionPolicy }

type TSListMetricsRequest  struct{ Prefix string; Limit int32; Consistency string }
type TSListMetricsResponse struct{ Metrics []string }
```

### Aggregation functions

`AggFunc` is a `uint8` with **explicit numeric values**, so the wire form is a
number and the numbering is part of the contract:

| Constant | Value |
|---|---|
| `AggNone` | 0 |
| `AggAvg` | 1 |
| `AggSum` | 2 |
| `AggMin` | 3 |
| `AggMax` | 4 |
| `AggCount` | 5 |
| `AggPercentile` | 6 |
| `AggFirst` | 7 |
| `AggLast` | 8 |

`AggPercentile` reads `TSRichQueryRequest.Percentile`; it is meaningless on
`TSAggregateRequest`, which carries no percentile field.

## Invariants

- **All timestamps are nanoseconds** (`TimestampNs`, `StartNs`, `EndNs`,
  `IntervalNs`, `DurationNs`, `ResolutionNs`). There is no second- or
  millisecond-based field anywhere in this surface.
- **Ranges are `[start_ns, end_ns)`** — start inclusive, end exclusive, matching
  every other range in the store.
- **`AggFunc` values are fixed numbers**, not names. Renumbering them is a
  breaking wire change.
- **`TSWrite` and `TSWritePoint` are both single-point writes** with distinct
  permissions (`timeseries:write`, `timeseries:write_point`). They are separate
  RPCs on every transport; treat `TSWritePoint` as the narrower grant.
- **Retention is per metric and is a downsampling policy**, not just a delete
  horizon: `duration_ns` bounds age, `resolution_ns` sets downsampled
  resolution, and `aggregation` chooses the function that produces the
  downsampled value.
- **`TSDeleteSeries` is bounded by its request**: metric, optionally tags,
  optionally a time range. With no range it deletes the whole series.
- **Time-series keys live under reserved prefixes** (`ts:`, `idx:metric:`,
  `idx:tag:`, `retention:`) and are granted **no** enumeration escape — the
  subsystem reads through `pkg/timeseries`, never through KV enumeration.
  They are therefore invisible to `kv list`/`kv scan` and unreachable by
  `kv restore`.
- **Tag cardinality is unbounded and unpoliced.** High-cardinality tags are the
  standard way to make a time-series store unusable, and nothing here stops it.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Built without `qrdb_ts` | capability absent; verbs and handlers unregistered | **yes** |
| Unknown metric on query | empty result (a metric is created by writing to it) | n/a |
| `end_ns <= start_ns` | invalid-argument | **yes** |
| Unknown `AggFunc` value | invalid-argument | **yes** |
| `AggPercentile` without `percentile` | invalid-argument | **yes** |
| Retention policy with `duration_ns <= 0` | invalid-argument | **yes** |
| Missing `timeseries:*` permission | permission denied | **yes** |
| Barrier sealed | refused | **yes** |

## Configuration

Retention is per metric through `TSSetRetention`, not a config file key.
Build flag: `WITH_QRDB_TS=1`.

## Persistence

- Points: composite keys under `ts:` — metric, tags and timestamp encoded so a
  metric's time range is a contiguous seek.
- Tag indexes: `idx:metric:` and `idx:tag:`.
- Retention policies: `retention:`.
- **Compatibility**: the composite key encoding and the `AggFunc` numbering are
  both on-disk/on-wire format. Changing either is a breaking change.

## Test strategy

- **Unit**, under `qrdb_ts`: write and batch write, range query boundary
  behaviour (`[start, end)`), tag filtering, each `AggFunc`, interval bucketing,
  `group_by` in rich query, percentile, delete with and without a range, and
  retention round-trip.
- **Error tests** per function.
- **No `time.Sleep`** — retention expiry is driven through the clock seam.
- **Integration**: time-series parity across transports in the standalone stage.

## Known gap — CLI does not cover the API

The `qrdb ts` command group exposes four of the ten capabilities: `write`,
`write-batch`, `query`, `aggregate`. Six have **no CLI verb**:
`TSWritePoint`, `TSRichQuery`, `TSDeleteSeries`, `TSGetRetention`,
`TSSetRetention`, `TSListMetrics`. They are reachable on all six transports
through the SDK. Recorded in [cli.md](cli.md) and [api.md](api.md).

## Observability

- Writes and deletes are audited through [../audit/](../audit/).
- `TSListMetrics` is the discovery surface — it is how an operator finds out
  what is being written, since metrics are created implicitly by writing.

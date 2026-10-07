# Time Series — User Guide

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-10-06
>
> "Target" sections describe the time-series half of the multi-model database
> program ([../databases/architecture.md](../databases/architecture.md)); they
> are approved by the owner on 2026-09-30 and not implemented. OD-n refers to that document's ruled decisions.

Test reference: `make integration-test-time-series`; coverage row: `test/integration/COVERAGE.md` `[time-series](../../docs/time-series/)`.

Store metrics — a name, a value, a nanosecond timestamp, and some tags — and
query them back by time range, filtered by tag, raw or aggregated.

## Prerequisites

- A daemon built with time-series support: `make build WITH_QRDB_TS=1`.
  Check with `qrdb admin capabilities` — `timeSeries` must be listed.
- A reachable server, client certificates, an unsealed barrier, and the
  `timeseries:*` permissions for what you do.

**Everything is nanoseconds.** One second is `1000000000`; one hour is
`3600000000000`.

## First run

```bash
qrdb ts write --metric cpu.usage --value 0.42 --tag host=web-01

qrdb ts query --metric cpu.usage --start 0 --end 9223372036854775807 --desc --limit 10
```

You do not create a metric — writing to it creates it.

## Common tasks

**Tag your points so you can slice them later.** Tags are the dimensions.

```bash
qrdb ts write --metric http.latency --value 0.031 \
     --tag host=web-01 --tag route=/api/v1/users --tag status=200
```

**Load a batch.**

```bash
cat > points.json <<'JSON'
[
  {"metric":"cpu.usage","value":0.42,"timestamp_ns":1755561600000000000,
   "tags":[{"key":"host","value":"web-01"}]},
  {"metric":"cpu.usage","value":0.51,"timestamp_ns":1755561660000000000,
   "tags":[{"key":"host","value":"web-01"}]}
]
JSON
qrdb ts write-batch --file points.json
```

**Query a window, filtered by tag.** The range is start-inclusive,
end-exclusive.

```bash
qrdb ts query --metric cpu.usage \
     --start 1755561600000000000 --end 1755565200000000000 \
     --tag host=web-01 --limit 1000 --output json
```

**Get hourly averages instead of raw points.** This is what you want for a
chart — it aggregates server side rather than shipping every point.

```bash
qrdb ts aggregate --metric cpu.usage \
     --start 1755561600000000000 --end 1755648000000000000 \
     --function avg --interval 3600000000000
```

Available functions: `avg`, `sum`, `min`, `max`, `count`, `percentile`.

**Compute a timestamp in your shell.**

```bash
NOW=$(date +%s%N)
HOUR_AGO=$(( NOW - 3600000000000 ))
qrdb ts query --metric cpu.usage --start "$HOUR_AGO" --end "$NOW"
```

## Things the CLI cannot do yet

Six capabilities exist on every API transport but have **no `qrdb ts` verb**:
grouping a query by tag (`TSRichQuery`), deleting a series
(`TSDeleteSeries`), reading and setting retention (`TSGetRetention`,
`TSSetRetention`), listing metrics (`TSListMetrics`), and the narrower-permission
single write (`TSWritePoint`).

Reach them through the SDK or the REST API. For example, listing metrics:

```bash
curl --cert client.pem --key client-key.pem --cacert ca.pem \
     https://db.example.com/ts/metrics
```

Setting a retention policy that keeps thirty days at hourly resolution:

```bash
curl --cert client.pem --key client-key.pem --cacert ca.pem \
     -X PUT https://db.example.com/ts/series/cpu.usage/retention \
     -d '{"policy":{"duration_ns":2592000000000000,
                    "resolution_ns":3600000000000,"aggregation":1}}'
```

`aggregation` is a number: 1 avg, 2 sum, 3 min, 4 max, 5 count, 6 percentile,
7 first, 8 last.

## Troubleshooting

**A query returns nothing and you are sure you wrote points.** Check the units.
`--start` and `--end` are nanoseconds; a second- or millisecond-based timestamp
lands so far in the past that nothing is in range. `date +%s%N` gives you
nanoseconds.

**The last point is missing from your range.** The range is `[start, end)` —
end is exclusive. Add one nanosecond, or widen the window.

**Tag filtering returns nothing.** Tag matching is exact on both key and value.
`--tag host=web-01` does not match a point tagged `Host=web-01`.

**Queries got slow and storage is huge.** Almost always tag cardinality:
a tag whose values are unique per request (a request ID, a user ID, a
timestamp) creates a new series per point. Nothing stops you from doing this,
so it has to be a discipline. Keep tags to bounded dimensions.

**You want data older than N days deleted.** Set a retention policy's
`duration_ns`; there is no CLI verb yet, use the REST call above. A
leader-driven sweep deletes points older than `duration_ns` within about a
minute of them aging out. **Downsampling is not applied yet**: a policy's
`resolution_ns`/`aggregation` are saved and returned but nothing replaces
an expired point with a downsampled one, so a policy that wanted to keep a
year at hourly resolution instead loses that year outright once
`duration_ns` passes — plan storage as if retention only deletes.

**Time-series calls fail with "not open" or "not configured".** Time series
works on every backend — memory, file, pebble and objstore — as long as the
binary was built with time-series support (`WITH_QRDB_TS=1`; see below). If
it still answers "not configured", confirm with `qrdb admin capabilities`.

**You deleted a series and more went than you expected.** `TSDeleteSeries` with
no time range deletes the whole series. Pass `start_ns`/`end_ns` to bound it.

**Time-series keys do not show up in `kv list`.** By design — `ts:`,
`idx:metric:`, `idx:tag:` and `retention:` are reserved internal prefixes, and
unlike some other reserved namespaces they get no enumeration escape at all.

**`qrdb ts` is not a command.** The binary was built without
`WITH_QRDB_TS=1`. Confirm with `qrdb admin capabilities`.

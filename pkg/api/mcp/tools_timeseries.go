// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// parseExactInt64 extracts an int64 from MCP arguments without float64
// precision loss. JSON numbers larger than 2^53 lose precision when parsed
// as float64 (the default in Go's encoding/json). This function converts
// the argument to a string first and then uses strconv.ParseInt for an
// exact result.
func parseExactInt64(req mcp.CallToolRequest, key string) (int64, error) {
	args := req.GetArguments()
	v, ok := args[key]
	if !ok {
		return 0, &RequiredParameterError{Parameter: key}
	}
	switch n := v.(type) {
	case json.Number:
		return n.Int64()
	case float64:
		// Convert via string to avoid float64 precision loss for large integers.
		// Clamp to int64 range: float64 can round values like MaxInt64 up to
		// MaxInt64+1, which would overflow strconv.ParseInt.
		if n >= float64(math.MaxInt64) {
			return math.MaxInt64, nil
		}
		if n <= float64(math.MinInt64) {
			return math.MinInt64, nil
		}
		return strconv.ParseInt(strconv.FormatFloat(n, 'f', 0, 64), 10, 64)
	case string:
		return strconv.ParseInt(n, 10, 64)
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	default:
		s := fmt.Sprintf("%v", v)
		return strconv.ParseInt(s, 10, 64)
	}
}

// aggFuncMap maps string aggregation function names to AggFunc constants
// for O(1) dispatch.
var aggFuncMap = map[string]timeseries.AggFunc{
	"avg":        timeseries.AggAvg,
	"sum":        timeseries.AggSum,
	"min":        timeseries.AggMin,
	"max":        timeseries.AggMax,
	"count":      timeseries.AggCount,
	"percentile": timeseries.AggPercentile,
	"first":      timeseries.AggFirst,
	"last":       timeseries.AggLast,
}

// timeSeriesTools returns the MCP tool registrations for time-series operations.
func timeSeriesTools(tsSvc service.TimeSeriesServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolTSWrite(),
			Perm:    permission.TSWrite,
			Handler: handleTSWrite(tsSvc),
		},
		{
			Tool:    toolTSWriteBatch(),
			Perm:    permission.TSWriteBatch,
			Handler: handleTSWriteBatch(tsSvc),
		},
		{
			Tool:    toolTSQuery(),
			Perm:    permission.TSQuery,
			Handler: handleTSQuery(tsSvc),
		},
		{
			Tool:    toolTSAggregate(),
			Perm:    permission.TSAggregate,
			Handler: handleTSAggregate(tsSvc),
		},
		{
			Tool:    toolTSDeleteSeries(),
			Perm:    permission.TSDeleteSeries,
			Handler: handleTSDeleteSeries(tsSvc),
		},
		{
			Tool:    toolTSGetRetentionPolicy(),
			Perm:    permission.TSGetRetention,
			Handler: handleTSGetRetentionPolicy(tsSvc),
		},
		{
			Tool:    toolTSSetRetentionPolicy(),
			Perm:    permission.TSSetRetention,
			Handler: handleTSSetRetentionPolicy(tsSvc),
		},
		{
			Tool:    toolTSListMetrics(),
			Perm:    permission.TSListMetrics,
			Handler: handleTSListMetrics(tsSvc),
		},
		{
			Tool:    toolTSWritePoint(),
			Perm:    permission.TSWritePoint,
			Handler: handleTSWritePoint(tsSvc),
		},
		{
			Tool:    toolTSRichQuery(),
			Perm:    permission.TSRichQuery,
			Handler: handleTSRichQuery(tsSvc),
		},
	}
}

// toolTSWrite defines the qrdb_ts_write tool schema.
func toolTSWrite() mcp.Tool {
	return mcp.NewTool("qrdb_ts_write",
		mcp.WithDescription("Store a single time-series data point through Raft consensus."),
		mcp.WithString("metric",
			mcp.Required(),
			mcp.Description("The metric name (e.g., 'cpu.usage', 'request.latency')."),
		),
		mcp.WithNumber("value",
			mcp.Required(),
			mcp.Description("The measured value."),
		),
		mcp.WithNumber("timestamp",
			mcp.Required(),
			mcp.Description("The measurement timestamp as nanoseconds since Unix epoch."),
		),
		mcp.WithString("tags",
			mcp.Description(`Optional JSON object of key-value tags for filtering and grouping. Example: {"host":"web-01","region":"us-east"}.`),
		),
	)
}

// handleTSWrite returns a tool handler that delegates to tsSvc.Write.
func handleTSWrite(tsSvc service.TimeSeriesServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tsSvc, rerr := resolveTS(ctx, tsSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		metric := mcp.ParseString(req, "metric", "")
		if metric == "" {
			return mcp.NewToolResultError("metric is required"), nil
		}
		value := mcp.ParseFloat64(req, "value", 0)
		tsNano := mcp.ParseInt64(req, "timestamp", 0)
		if tsNano == 0 {
			return mcp.NewToolResultError("timestamp is required"), nil
		}
		timestamp := time.Unix(0, tsNano)

		tags, err := parseTags(req)
		if err != nil {
			return mcp.NewToolResultError("invalid tags JSON: " + err.Error()), nil
		}

		if writeErr := tsSvc.Write(ctx, metric, value, timestamp, tags); writeErr != nil {
			return nil, &ToolError{Tool: "qrdb_ts_write", Err: writeErr}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolTSWriteBatch defines the qrdb_ts_write_batch tool schema.
func toolTSWriteBatch() mcp.Tool {
	return mcp.NewTool("qrdb_ts_write_batch",
		mcp.WithDescription("Store multiple time-series data points atomically through Raft consensus."),
		mcp.WithString("points",
			mcp.Required(),
			mcp.Description(`JSON array of data points. Each element: {"metric":"<name>","value":<float>,"timestamp":<nanoseconds>,"tags":{"key":"value"}}.`),
		),
	)
}

// tsPointJSON is the JSON representation of a time-series data point
// for batch write operations.
type tsPointJSON struct {
	Metric    string            `json:"metric"`
	Value     float64           `json:"value"`
	Timestamp int64             `json:"timestamp"`
	Tags      map[string]string `json:"tags"`
}

// handleTSWriteBatch returns a tool handler that delegates to
// tsSvc.WriteBatch.
func handleTSWriteBatch(tsSvc service.TimeSeriesServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tsSvc, rerr := resolveTS(ctx, tsSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		pointsStr := mcp.ParseString(req, "points", "")
		if pointsStr == "" {
			return mcp.NewToolResultError("points is required"), nil
		}

		var pointsJSON []tsPointJSON
		if err := json.Unmarshal([]byte(pointsStr), &pointsJSON); err != nil {
			return mcp.NewToolResultError("invalid points JSON: " + err.Error()), nil
		}

		if len(pointsJSON) == 0 {
			return mcp.NewToolResultError("points must not be empty"), nil
		}

		points := make([]timeseries.DataPoint, len(pointsJSON))
		for i, p := range pointsJSON {
			points[i] = timeseries.DataPoint{
				Metric:    p.Metric,
				Value:     p.Value,
				Timestamp: time.Unix(0, p.Timestamp),
				Tags:      p.Tags,
			}
		}

		if err := tsSvc.WriteBatch(ctx, points); err != nil {
			return nil, &ToolError{Tool: "qrdb_ts_write_batch", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolTSQuery defines the qrdb_ts_query tool schema.
func toolTSQuery() mcp.Tool {
	return mcp.NewTool("qrdb_ts_query",
		mcp.WithDescription("Query time-series data points for a metric within a time range."),
		mcp.WithString("metric",
			mcp.Required(),
			mcp.Description("The metric name to query."),
		),
		mcp.WithNumber("start",
			mcp.Required(),
			mcp.Description("Start of time range as nanoseconds since Unix epoch (inclusive)."),
		),
		mcp.WithNumber("end",
			mcp.Required(),
			mcp.Description("End of time range as nanoseconds since Unix epoch (exclusive)."),
		),
		mcp.WithString("tags",
			mcp.Description(`Optional JSON object of tag filters. Only data points matching all tags are returned. Example: {"host":"web-01"}.`),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of data points to return. 0 for unlimited."),
		),
		mcp.WithBoolean("descending",
			mcp.Description("Return data points in reverse chronological order."),
		),
		mcp.WithString("consistency",
			mcp.Description("Read consistency: strong|linearizable|eventual"),
		),
	)
}

// handleTSQuery returns a tool handler that delegates to tsSvc.Query or
// tsSvc.QueryWithConsistency when a consistency arg is present.
func handleTSQuery(tsSvc service.TimeSeriesServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tsSvc, rerr := resolveTS(ctx, tsSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		metric := mcp.ParseString(req, "metric", "")
		if metric == "" {
			return mcp.NewToolResultError("metric is required"), nil
		}
		startNano, startErr := parseExactInt64(req, "start")
		if startErr != nil {
			return mcp.NewToolResultError("start is required"), nil
		}
		endNano, endErr := parseExactInt64(req, "end")
		if endErr != nil {
			return mcp.NewToolResultError("end is required"), nil
		}

		start := time.Unix(0, startNano)
		end := time.Unix(0, endNano)

		var opts []timeseries.TSQueryOption

		tags, err := parseTags(req)
		if err != nil {
			return mcp.NewToolResultError("invalid tags JSON: " + err.Error()), nil
		}
		if len(tags) > 0 {
			opts = append(opts, timeseries.WithTags(tags))
		}

		if limit := mcp.ParseInt(req, "limit", 0); limit > 0 {
			opts = append(opts, timeseries.WithTSLimit(limit))
		}
		if mcp.ParseBoolean(req, "descending", false) {
			opts = append(opts, timeseries.WithDescending())
		}

		ropts, roptsSet, roptsErr := readOptsFromArg(req)
		if roptsErr != nil {
			return mcp.NewToolResultError("invalid consistency: " + roptsErr.Error()), nil
		}

		var points []timeseries.DataPoint
		var queryErr error
		if roptsSet {
			points, queryErr = tsSvc.QueryWithConsistency(ctx, metric, start, end, ropts, opts...)
		} else {
			points, queryErr = tsSvc.Query(ctx, metric, start, end, opts...)
		}
		if queryErr != nil {
			return nil, &ToolError{Tool: "qrdb_ts_query", Err: queryErr}
		}

		data, jsonErr := json.Marshal(points)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_ts_query", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolTSAggregate defines the qrdb_ts_aggregate tool schema.
func toolTSAggregate() mcp.Tool {
	return mcp.NewTool("qrdb_ts_aggregate",
		mcp.WithDescription("Compute an aggregation function over time-series data points within a time range, grouped by a specified interval."),
		mcp.WithString("metric",
			mcp.Required(),
			mcp.Description("The metric name to aggregate."),
		),
		mcp.WithNumber("start",
			mcp.Required(),
			mcp.Description("Start of time range as nanoseconds since Unix epoch (inclusive)."),
		),
		mcp.WithNumber("end",
			mcp.Required(),
			mcp.Description("End of time range as nanoseconds since Unix epoch (exclusive)."),
		),
		mcp.WithString("func",
			mcp.Required(),
			mcp.Description("Aggregation function: avg, sum, min, max, count, percentile, first, or last."),
		),
		mcp.WithNumber("interval_ns",
			mcp.Required(),
			mcp.Description("Aggregation interval in nanoseconds."),
		),
		mcp.WithString("consistency",
			mcp.Description("Read consistency: strong|linearizable|eventual"),
		),
	)
}

// handleTSAggregate returns a tool handler that delegates to
// tsSvc.Aggregate or tsSvc.AggregateWithConsistency when a consistency
// arg is present.
func handleTSAggregate(tsSvc service.TimeSeriesServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tsSvc, rerr := resolveTS(ctx, tsSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		metric := mcp.ParseString(req, "metric", "")
		if metric == "" {
			return mcp.NewToolResultError("metric is required"), nil
		}
		startNano, startErr := parseExactInt64(req, "start")
		if startErr != nil {
			return mcp.NewToolResultError("start is required"), nil
		}
		endNano, endErr := parseExactInt64(req, "end")
		if endErr != nil {
			return mcp.NewToolResultError("end is required"), nil
		}
		funcName := mcp.ParseString(req, "func", "")
		if funcName == "" {
			return mcp.NewToolResultError("func is required"), nil
		}
		intervalNs, intervalErr := parseExactInt64(req, "interval_ns")
		if intervalErr != nil {
			return mcp.NewToolResultError("interval_ns is required"), nil
		}

		aggFunc, ok := aggFuncMap[funcName]
		if !ok {
			return mcp.NewToolResultError("invalid func: " + funcName + "; must be one of avg, sum, min, max, count, percentile, first, last"), nil
		}

		ropts, roptsSet, roptsErr := readOptsFromArg(req)
		if roptsErr != nil {
			return mcp.NewToolResultError("invalid consistency: " + roptsErr.Error()), nil
		}

		start := time.Unix(0, startNano)
		end := time.Unix(0, endNano)
		interval := time.Duration(intervalNs)

		var results []timeseries.AggResult
		var err error
		if roptsSet {
			results, err = tsSvc.AggregateWithConsistency(ctx, metric, start, end, aggFunc, interval, ropts)
		} else {
			results, err = tsSvc.Aggregate(ctx, metric, start, end, aggFunc, interval)
		}
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ts_aggregate", Err: err}
		}

		data, jsonErr := json.Marshal(results)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_ts_aggregate", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolTSDeleteSeries defines the qrdb_ts_delete_series tool schema.
func toolTSDeleteSeries() mcp.Tool {
	return mcp.NewTool("qrdb_ts_delete_series",
		mcp.WithDescription("Delete time-series data points matching the specified metric, tags, and optional time range."),
		mcp.WithString("metric",
			mcp.Required(),
			mcp.Description("The metric name to delete data for."),
		),
		mcp.WithString("tags",
			mcp.Description(`Optional JSON object of tag filters. Only data points matching all tags are deleted. When empty, all series for the metric are deleted. Example: {"host":"web-01"}.`),
		),
		mcp.WithNumber("start",
			mcp.Description("Start of time range as nanoseconds since Unix epoch (inclusive). 0 or omitted means from the beginning."),
		),
		mcp.WithNumber("end",
			mcp.Description("End of time range as nanoseconds since Unix epoch (exclusive). 0 or omitted means to the end."),
		),
	)
}

// handleTSDeleteSeries returns a tool handler that delegates to
// tsSvc.DeleteSeries.
func handleTSDeleteSeries(tsSvc service.TimeSeriesServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tsSvc, rerr := resolveTS(ctx, tsSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		metric := mcp.ParseString(req, "metric", "")
		if metric == "" {
			return mcp.NewToolResultError("metric is required"), nil
		}

		tags, err := parseTags(req)
		if err != nil {
			return mcp.NewToolResultError("invalid tags JSON: " + err.Error()), nil
		}

		deleteReq := &timeseries.DeleteRequest{
			Metric: metric,
			Tags:   tags,
		}

		if startNano := mcp.ParseInt64(req, "start", 0); startNano != 0 {
			deleteReq.StartTime = time.Unix(0, startNano)
		}
		if endNano := mcp.ParseInt64(req, "end", 0); endNano != 0 {
			deleteReq.EndTime = time.Unix(0, endNano)
		}

		if deleteErr := tsSvc.DeleteSeries(ctx, deleteReq); deleteErr != nil {
			return nil, &ToolError{Tool: "qrdb_ts_delete_series", Err: deleteErr}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolTSGetRetentionPolicy defines the qrdb_ts_get_retention_policy tool schema.
func toolTSGetRetentionPolicy() mcp.Tool {
	return mcp.NewTool("qrdb_ts_get_retention_policy",
		mcp.WithDescription("Retrieve the retention policy for a specific metric, including duration, resolution, and aggregation settings."),
		mcp.WithString("metric",
			mcp.Required(),
			mcp.Description("The metric name to retrieve the retention policy for."),
		),
	)
}

// handleTSGetRetentionPolicy returns a tool handler that delegates to
// tsSvc.GetRetentionPolicy.
func handleTSGetRetentionPolicy(tsSvc service.TimeSeriesServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tsSvc, rerr := resolveTS(ctx, tsSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		metric := mcp.ParseString(req, "metric", "")
		if metric == "" {
			return mcp.NewToolResultError("metric is required"), nil
		}

		policy, err := tsSvc.GetRetentionPolicy(metric)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ts_get_retention_policy", Err: err}
		}

		data, jsonErr := json.Marshal(policy)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_ts_get_retention_policy", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolTSSetRetentionPolicy defines the qrdb_ts_set_retention_policy tool schema.
func toolTSSetRetentionPolicy() mcp.Tool {
	return mcp.NewTool("qrdb_ts_set_retention_policy",
		mcp.WithDescription("Set the retention policy for a metric including data retention duration, downsampling resolution, and aggregation function."),
		mcp.WithString("metric",
			mcp.Required(),
			mcp.Description("The metric name to set the retention policy for."),
		),
		mcp.WithNumber("duration_seconds",
			mcp.Required(),
			mcp.Description("How long to retain data in seconds. 0 means infinite retention."),
		),
		mcp.WithNumber("resolution_seconds",
			mcp.Description("Downsampling resolution in seconds. 0 or omitted means no downsampling."),
		),
		mcp.WithString("aggregation",
			mcp.Description("Aggregation function for downsampling: avg, sum, min, max, count, percentile, first, or last. Required when resolution_seconds is set."),
		),
	)
}

// handleTSSetRetentionPolicy returns a tool handler that delegates to
// tsSvc.SetRetentionPolicy.
func handleTSSetRetentionPolicy(tsSvc service.TimeSeriesServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tsSvc, rerr := resolveTS(ctx, tsSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		metric := mcp.ParseString(req, "metric", "")
		if metric == "" {
			return mcp.NewToolResultError("metric is required"), nil
		}

		durationSec := mcp.ParseInt64(req, "duration_seconds", 0)
		resolutionSec := mcp.ParseInt64(req, "resolution_seconds", 0)

		policy := &timeseries.RetentionPolicy{
			Duration:   time.Duration(durationSec) * time.Second,
			Resolution: time.Duration(resolutionSec) * time.Second,
		}

		aggName := mcp.ParseString(req, "aggregation", "")
		if aggName != "" {
			aggFunc, ok := aggFuncMap[aggName]
			if !ok {
				return mcp.NewToolResultError("invalid aggregation: " + aggName + "; must be one of avg, sum, min, max, count, percentile, first, last"), nil
			}
			policy.Aggregation = aggFunc
		}

		if setErr := tsSvc.SetRetentionPolicy(metric, policy); setErr != nil {
			return nil, &ToolError{Tool: "qrdb_ts_set_retention_policy", Err: setErr}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolTSListMetrics defines the qrdb_ts_list_metrics tool schema.
func toolTSListMetrics() mcp.Tool {
	return mcp.NewTool("qrdb_ts_list_metrics",
		mcp.WithDescription("List available metric names with optional prefix filtering and result limiting."),
		mcp.WithString("prefix",
			mcp.Description("Filter metrics by name prefix. When empty, all metrics are returned."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of metrics to return. Defaults to 100 when 0 or omitted."),
		),
		mcp.WithString("consistency",
			mcp.Description("Read consistency: strong|linearizable|eventual"),
		),
	)
}

// handleTSListMetrics returns a tool handler that delegates to
// tsSvc.ListMetrics or tsSvc.ListMetricsWithConsistency when a
// consistency arg is present.
func handleTSListMetrics(tsSvc service.TimeSeriesServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tsSvc, rerr := resolveTS(ctx, tsSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		prefix := mcp.ParseString(req, "prefix", "")
		limit := mcp.ParseInt(req, "limit", 0)
		if limit <= 0 {
			limit = 100
		}

		ropts, roptsSet, roptsErr := readOptsFromArg(req)
		if roptsErr != nil {
			return mcp.NewToolResultError("invalid consistency: " + roptsErr.Error()), nil
		}

		var metrics []string
		var err error
		if roptsSet {
			metrics, err = tsSvc.ListMetricsWithConsistency(ctx, prefix, limit, ropts)
		} else {
			metrics, err = tsSvc.ListMetrics(prefix, limit)
		}
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ts_list_metrics", Err: err}
		}

		data, jsonErr := json.Marshal(metrics)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_ts_list_metrics", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolTSWritePoint defines the qrdb_ts_write_point tool schema.
func toolTSWritePoint() mcp.Tool {
	return mcp.NewTool("qrdb_ts_write_point",
		mcp.WithDescription("Store a single validated time-series data point through Raft consensus. Accepts a structured DataPoint with metric, value, timestamp, and optional tags."),
		mcp.WithString("metric",
			mcp.Required(),
			mcp.Description("The metric name (e.g., 'cpu.usage', 'request.latency')."),
		),
		mcp.WithNumber("value",
			mcp.Required(),
			mcp.Description("The measured value."),
		),
		mcp.WithNumber("timestamp",
			mcp.Required(),
			mcp.Description("The measurement timestamp as nanoseconds since Unix epoch."),
		),
		mcp.WithString("tags",
			mcp.Description(`Optional JSON object of key-value tags for filtering and grouping. Example: {"host":"web-01","region":"us-east"}.`),
		),
	)
}

// handleTSWritePoint returns a tool handler that delegates to
// tsSvc.WritePoint.
func handleTSWritePoint(tsSvc service.TimeSeriesServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tsSvc, rerr := resolveTS(ctx, tsSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		metric := mcp.ParseString(req, "metric", "")
		if metric == "" {
			return mcp.NewToolResultError("metric is required"), nil
		}
		value := mcp.ParseFloat64(req, "value", 0)
		tsNano := mcp.ParseInt64(req, "timestamp", 0)
		if tsNano == 0 {
			return mcp.NewToolResultError("timestamp is required"), nil
		}
		timestamp := time.Unix(0, tsNano)

		tags, err := parseTags(req)
		if err != nil {
			return mcp.NewToolResultError("invalid tags JSON: " + err.Error()), nil
		}

		point := &timeseries.DataPoint{
			Metric:    metric,
			Value:     value,
			Timestamp: timestamp,
			Tags:      tags,
		}

		if writeErr := tsSvc.WritePoint(ctx, point); writeErr != nil {
			return nil, &ToolError{Tool: "qrdb_ts_write_point", Err: writeErr}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolTSRichQuery defines the qrdb_ts_rich_query tool schema.
func toolTSRichQuery() mcp.Tool {
	return mcp.NewTool("qrdb_ts_rich_query",
		mcp.WithDescription("Perform a rich time-series query with filtering, aggregation, downsampling, and grouping. Returns points and optional aggregated values."),
		mcp.WithString("metric",
			mcp.Required(),
			mcp.Description("The metric name to query."),
		),
		mcp.WithNumber("start",
			mcp.Required(),
			mcp.Description("Start of time range as nanoseconds since Unix epoch (inclusive)."),
		),
		mcp.WithNumber("end",
			mcp.Required(),
			mcp.Description("End of time range as nanoseconds since Unix epoch (exclusive)."),
		),
		mcp.WithString("tags",
			mcp.Description(`Optional JSON object of tag filters. Only data points matching all tags are returned. Example: {"host":"web-01"}.`),
		),
		mcp.WithString("aggregation",
			mcp.Description("Aggregation function: avg, sum, min, max, count, percentile, first, or last. When omitted, raw data points are returned."),
		),
		mcp.WithNumber("interval_ns",
			mcp.Description("Downsampling interval in nanoseconds. Required when aggregation is set."),
		),
		mcp.WithString("group_by",
			mcp.Description(`Optional JSON array of tag keys to group results by. Example: ["host","region"].`),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of data points to return. 0 for unlimited."),
		),
		mcp.WithNumber("percentile",
			mcp.Description("Percentile value (0-100) when aggregation is 'percentile'."),
		),
		mcp.WithString("consistency",
			mcp.Description("Read consistency: strong|linearizable|eventual"),
		),
	)
}

// handleTSRichQuery returns a tool handler that delegates to
// tsSvc.QueryRich or tsSvc.QueryRichWithConsistency when a consistency
// arg is present.
func handleTSRichQuery(tsSvc service.TimeSeriesServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tsSvc, rerr := resolveTS(ctx, tsSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		metric := mcp.ParseString(req, "metric", "")
		if metric == "" {
			return mcp.NewToolResultError("metric is required"), nil
		}
		startNano, startErr := parseExactInt64(req, "start")
		if startErr != nil {
			return mcp.NewToolResultError("start is required"), nil
		}
		endNano, endErr := parseExactInt64(req, "end")
		if endErr != nil {
			return mcp.NewToolResultError("end is required"), nil
		}

		tags, err := parseTags(req)
		if err != nil {
			return mcp.NewToolResultError("invalid tags JSON: " + err.Error()), nil
		}

		qr := &timeseries.QueryRequest{
			Metric:    metric,
			Tags:      tags,
			StartTime: time.Unix(0, startNano),
			EndTime:   time.Unix(0, endNano),
			Limit:     mcp.ParseInt(req, "limit", 0),
		}

		aggName := mcp.ParseString(req, "aggregation", "")
		if aggName != "" {
			aggFunc, ok := aggFuncMap[aggName]
			if !ok {
				return mcp.NewToolResultError("invalid aggregation: " + aggName + "; must be one of avg, sum, min, max, count, percentile, first, last"), nil
			}
			qr.Aggregation = aggFunc
		}

		intervalNs := mcp.ParseInt64(req, "interval_ns", 0)
		if intervalNs > 0 {
			qr.Interval = time.Duration(intervalNs)
		}

		groupByStr := mcp.ParseString(req, "group_by", "")
		if groupByStr != "" {
			var groupBy []string
			if jsonErr := json.Unmarshal([]byte(groupByStr), &groupBy); jsonErr != nil {
				return mcp.NewToolResultError("invalid group_by JSON: " + jsonErr.Error()), nil
			}
			qr.GroupBy = groupBy
		}

		qr.Percentile = mcp.ParseFloat64(req, "percentile", 0)

		ropts, roptsSet, roptsErr := readOptsFromArg(req)
		if roptsErr != nil {
			return mcp.NewToolResultError("invalid consistency: " + roptsErr.Error()), nil
		}

		var result *timeseries.QueryResult
		var queryErr error
		if roptsSet {
			result, queryErr = tsSvc.QueryRichWithConsistency(ctx, qr, ropts)
		} else {
			result, queryErr = tsSvc.QueryRich(ctx, qr)
		}
		if queryErr != nil {
			return nil, &ToolError{Tool: "qrdb_ts_rich_query", Err: queryErr}
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_ts_rich_query", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// parseTags extracts the optional "tags" parameter from a request and
// parses it as a JSON object of string key-value pairs. Returns nil
// when the parameter is absent or empty.
func parseTags(req mcp.CallToolRequest) (map[string]string, error) {
	tagsStr := mcp.ParseString(req, "tags", "")
	if tagsStr == "" {
		return nil, nil
	}

	var tags map[string]string
	if err := json.Unmarshal([]byte(tagsStr), &tags); err != nil {
		return nil, err
	}
	return tags, nil
}

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
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.TimeSeriesClient = (*Transport)(nil)

// sdkAggFuncToString maps SDK AggFunc constants to the string names
// expected by the MCP server tool parameters for O(1) dispatch.
var sdkAggFuncToString = map[transport.AggFunc]string{
	transport.AggAvg:        "avg",
	transport.AggSum:        "sum",
	transport.AggMin:        "min",
	transport.AggMax:        "max",
	transport.AggCount:      "count",
	transport.AggPercentile: "percentile",
	transport.AggFirst:      "first",
	transport.AggLast:       "last",
}

// ---------------------------------------------------------------------------
// Server-side JSON decode types for time-series responses
// ---------------------------------------------------------------------------

// serverDataPoint mirrors timeseries.DataPoint for JSON decoding from
// the MCP server. The server uses time.Time for timestamps (marshaled
// as RFC 3339) and map[string]string for tags.
type serverDataPoint struct {
	Metric    string            `json:"metric"`
	Value     float64           `json:"value"`
	Timestamp time.Time         `json:"timestamp"`
	Tags      map[string]string `json:"tags,omitempty"`
}

// serverAggResult mirrors timeseries.AggResult for JSON decoding. The
// server-side struct has no JSON tags, so Go's default PascalCase
// encoding is used.
type serverAggResult struct {
	Timestamp time.Time `json:"Timestamp"`
	Value     float64   `json:"Value"`
	Count     int64     `json:"Count"`
}

// serverRetentionPolicy mirrors timeseries.RetentionPolicy for JSON
// decoding. Duration and Resolution are time.Duration values encoded
// as integers (nanoseconds) by json.Marshal.
type serverRetentionPolicy struct {
	Duration    int64 `json:"duration"`
	Resolution  int64 `json:"resolution,omitempty"`
	Aggregation int   `json:"aggregation,omitempty"`
}

// serverQueryResult mirrors timeseries.QueryResult for JSON decoding.
type serverQueryResult struct {
	Metric          string                 `json:"metric"`
	Tags            map[string]string      `json:"tags,omitempty"`
	Points          []*serverDataPoint     `json:"points"`
	AggregatedValue *float64               `json:"aggregated_value,omitempty"`
	GroupedResults  []*serverGroupedResult `json:"grouped_results,omitempty"`
}

// serverGroupedResult mirrors timeseries.GroupedResult for JSON decoding.
type serverGroupedResult struct {
	Tags            map[string]string  `json:"tags"`
	Points          []*serverDataPoint `json:"points,omitempty"`
	AggregatedValue *float64           `json:"aggregated_value,omitempty"`
}

// ---------------------------------------------------------------------------
// Tag conversion helpers (MCP uses map[string]string, SDK uses []TSTag)
// ---------------------------------------------------------------------------

// tagsSliceToMap converts a slice of transport TSTag to a map for MCP
// tool parameter encoding.
func tagsSliceToMap(tags []transport.TSTag) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[t.Key] = t.Value
	}
	return m
}

// tagsMapToSlice converts a map from MCP JSON decoding to a slice of
// transport TSTag.
func tagsMapToSlice(m map[string]string) []transport.TSTag {
	if len(m) == 0 {
		return nil
	}
	tags := make([]transport.TSTag, 0, len(m))
	for k, v := range m {
		tags = append(tags, transport.TSTag{Key: k, Value: v})
	}
	return tags
}

// tagsMapToJSON serializes a tag map as a JSON string for MCP tool
// parameters. Returns an empty string when the map is nil or empty.
func tagsMapToJSON(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// serverDataPointToTransport converts a server-side DataPoint to the
// SDK transport representation.
func serverDataPointToTransport(p *serverDataPoint) transport.TSDataPoint {
	return transport.TSDataPoint{
		Metric:      p.Metric,
		Value:       p.Value,
		TimestampNs: p.Timestamp.UnixNano(),
		Tags:        tagsMapToSlice(p.Tags),
	}
}

// ---------------------------------------------------------------------------
// TimeSeriesClient implementation
// ---------------------------------------------------------------------------

// TSWrite stores a single time-series data point through Raft consensus.
// Calls the qrdb_ts_write MCP tool.
func (t *Transport) TSWrite(ctx context.Context, point *transport.TSDataPoint) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	args := map[string]any{
		"metric":    point.Metric,
		"value":     point.Value,
		"timestamp": point.TimestampNs,
	}

	tagsMap := tagsSliceToMap(point.Tags)
	tagsJSON, err := tagsMapToJSON(tagsMap)
	if err != nil {
		return &transport.RequestError{Method: "TSWrite", Err: &jsonDecodeError{Err: err}}
	}
	if tagsJSON != "" {
		args["tags"] = tagsJSON
	}

	_, callErr := t.callTool(ctx, "qrdb_ts_write", args)
	return callErr
}

// TSWriteBatch stores multiple time-series data points atomically
// through Raft consensus. Calls the qrdb_ts_write_batch MCP tool.
func (t *Transport) TSWriteBatch(ctx context.Context, points []transport.TSDataPoint) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	type pointJSON struct {
		Metric    string            `json:"metric"`
		Value     float64           `json:"value"`
		Timestamp int64             `json:"timestamp"`
		Tags      map[string]string `json:"tags,omitempty"`
	}

	batch := make([]pointJSON, len(points))
	for i, p := range points {
		batch[i] = pointJSON{
			Metric:    p.Metric,
			Value:     p.Value,
			Timestamp: p.TimestampNs,
			Tags:      tagsSliceToMap(p.Tags),
		}
	}

	data, err := json.Marshal(batch)
	if err != nil {
		return &transport.RequestError{Method: "TSWriteBatch", Err: &jsonDecodeError{Err: err}}
	}

	_, callErr := t.callTool(ctx, "qrdb_ts_write_batch", map[string]any{
		"points": string(data),
	})
	return callErr
}

// TSQuery retrieves data points for a metric within a time range.
// Calls the qrdb_ts_query MCP tool and parses the JSON array of
// data points from the server response. When req.Consistency is set,
// it is forwarded as the "consistency" tool argument.
func (t *Transport) TSQuery(ctx context.Context, req *transport.TSQueryRequest) (*transport.TSQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"metric": req.Metric,
		"start":  req.StartNs,
		"end":    req.EndNs,
	}

	tagsMap := tagsSliceToMap(req.Tags)
	tagsJSON, err := tagsMapToJSON(tagsMap)
	if err != nil {
		return nil, &transport.RequestError{Method: "TSQuery", Err: &jsonDecodeError{Err: err}}
	}
	if tagsJSON != "" {
		args["tags"] = tagsJSON
	}
	if req.Limit > 0 {
		args["limit"] = int64(req.Limit)
	}
	if req.Descending {
		args["descending"] = true
	}
	if req.Consistency != "" {
		args["consistency"] = req.Consistency
	}

	text, callErr := t.callTool(ctx, "qrdb_ts_query", args)
	if callErr != nil {
		return nil, callErr
	}

	var serverPoints []serverDataPoint
	if jsonErr := json.Unmarshal([]byte(text), &serverPoints); jsonErr != nil {
		return nil, &transport.RequestError{Method: "TSQuery", Err: &jsonDecodeError{Err: jsonErr}}
	}

	points := make([]transport.TSDataPoint, len(serverPoints))
	for i := range serverPoints {
		points[i] = serverDataPointToTransport(&serverPoints[i])
	}

	return &transport.TSQueryResponse{Points: points}, nil
}

// TSAggregate computes an aggregation function over time-series data
// points within a time range, grouped by a specified interval. Calls
// the qrdb_ts_aggregate MCP tool. When req.Consistency is set, it is
// forwarded as the "consistency" tool argument.
func (t *Transport) TSAggregate(ctx context.Context, req *transport.TSAggregateRequest) (*transport.TSAggregateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	funcName, ok := sdkAggFuncToString[req.Function]
	if !ok {
		return nil, &transport.RequestError{
			Method: "TSAggregate",
			Err:    &toolResultError{Tool: "qrdb_ts_aggregate", Message: "unsupported aggregation function"},
		}
	}

	args := map[string]any{
		"metric":      req.Metric,
		"start":       req.StartNs,
		"end":         req.EndNs,
		"func":        funcName,
		"interval_ns": req.IntervalNs,
	}
	if req.Consistency != "" {
		args["consistency"] = req.Consistency
	}

	text, callErr := t.callTool(ctx, "qrdb_ts_aggregate", args)
	if callErr != nil {
		return nil, callErr
	}

	var serverResults []serverAggResult
	if jsonErr := json.Unmarshal([]byte(text), &serverResults); jsonErr != nil {
		return nil, &transport.RequestError{Method: "TSAggregate", Err: &jsonDecodeError{Err: jsonErr}}
	}

	results := make([]transport.TSAggResult, len(serverResults))
	for i, r := range serverResults {
		results[i] = transport.TSAggResult{
			TimestampNs: r.Timestamp.UnixNano(),
			Value:       r.Value,
			Count:       r.Count,
		}
	}

	return &transport.TSAggregateResponse{Results: results}, nil
}

// TSWritePoint stores a single validated data point through Raft
// consensus. Calls the qrdb_ts_write_point MCP tool.
func (t *Transport) TSWritePoint(ctx context.Context, point *transport.TSDataPoint) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	args := map[string]any{
		"metric":    point.Metric,
		"value":     point.Value,
		"timestamp": point.TimestampNs,
	}

	tagsMap := tagsSliceToMap(point.Tags)
	tagsJSON, err := tagsMapToJSON(tagsMap)
	if err != nil {
		return &transport.RequestError{Method: "TSWritePoint", Err: &jsonDecodeError{Err: err}}
	}
	if tagsJSON != "" {
		args["tags"] = tagsJSON
	}

	_, callErr := t.callTool(ctx, "qrdb_ts_write_point", args)
	return callErr
}

// TSRichQuery performs a rich query with aggregation, downsampling,
// and grouping. Calls the qrdb_ts_rich_query MCP tool and parses the
// JSON response containing points, aggregated values, and grouped
// results. When req.Consistency is set, it is forwarded as the
// "consistency" tool argument.
func (t *Transport) TSRichQuery(ctx context.Context, req *transport.TSRichQueryRequest) (*transport.TSRichQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"metric": req.Metric,
		"start":  req.StartNs,
		"end":    req.EndNs,
	}

	tagsMap := tagsSliceToMap(req.Tags)
	tagsJSON, err := tagsMapToJSON(tagsMap)
	if err != nil {
		return nil, &transport.RequestError{Method: "TSRichQuery", Err: &jsonDecodeError{Err: err}}
	}
	if tagsJSON != "" {
		args["tags"] = tagsJSON
	}

	if req.Aggregation != transport.AggNone {
		funcName, ok := sdkAggFuncToString[req.Aggregation]
		if !ok {
			return nil, &transport.RequestError{
				Method: "TSRichQuery",
				Err:    &toolResultError{Tool: "qrdb_ts_rich_query", Message: "unsupported aggregation function"},
			}
		}
		args["aggregation"] = funcName
	}

	if req.IntervalNs > 0 {
		args["interval_ns"] = req.IntervalNs
	}

	if len(req.GroupBy) > 0 {
		groupByJSON, jsonErr := json.Marshal(req.GroupBy)
		if jsonErr != nil {
			return nil, &transport.RequestError{Method: "TSRichQuery", Err: &jsonDecodeError{Err: jsonErr}}
		}
		args["group_by"] = string(groupByJSON)
	}

	if req.Limit > 0 {
		args["limit"] = int64(req.Limit)
	}

	if req.Percentile > 0 {
		args["percentile"] = req.Percentile
	}

	if req.Consistency != "" {
		args["consistency"] = req.Consistency
	}

	text, callErr := t.callTool(ctx, "qrdb_ts_rich_query", args)
	if callErr != nil {
		return nil, callErr
	}

	var serverResult serverQueryResult
	if jsonErr := json.Unmarshal([]byte(text), &serverResult); jsonErr != nil {
		return nil, &transport.RequestError{Method: "TSRichQuery", Err: &jsonDecodeError{Err: jsonErr}}
	}

	points := make([]transport.TSDataPoint, len(serverResult.Points))
	for i, p := range serverResult.Points {
		points[i] = serverDataPointToTransport(p)
	}

	grouped := make([]transport.TSGroupedResult, len(serverResult.GroupedResults))
	for i, g := range serverResult.GroupedResults {
		gPoints := make([]transport.TSDataPoint, len(g.Points))
		for j, p := range g.Points {
			gPoints[j] = serverDataPointToTransport(p)
		}
		grouped[i] = transport.TSGroupedResult{
			Tags:            tagsMapToSlice(g.Tags),
			Points:          gPoints,
			AggregatedValue: g.AggregatedValue,
		}
	}

	return &transport.TSRichQueryResponse{
		Metric:          serverResult.Metric,
		Tags:            tagsMapToSlice(serverResult.Tags),
		Points:          points,
		AggregatedValue: serverResult.AggregatedValue,
		GroupedResults:  grouped,
	}, nil
}

// TSDeleteSeries deletes time-series data points matching the
// specified metric, tags, and optional time range. Calls the
// qrdb_ts_delete_series MCP tool.
func (t *Transport) TSDeleteSeries(ctx context.Context, req *transport.TSDeleteRequest) (*transport.TSDeleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"metric": req.Metric,
	}

	tagsMap := tagsSliceToMap(req.Tags)
	tagsJSON, err := tagsMapToJSON(tagsMap)
	if err != nil {
		return nil, &transport.RequestError{Method: "TSDeleteSeries", Err: &jsonDecodeError{Err: err}}
	}
	if tagsJSON != "" {
		args["tags"] = tagsJSON
	}

	if req.StartNs != 0 {
		args["start"] = req.StartNs
	}
	if req.EndNs != 0 {
		args["end"] = req.EndNs
	}

	_, callErr := t.callTool(ctx, "qrdb_ts_delete_series", args)
	if callErr != nil {
		return nil, callErr
	}

	return &transport.TSDeleteResponse{}, nil
}

// TSGetRetention retrieves the retention policy for a metric.
// Calls the qrdb_ts_get_retention_policy MCP tool and parses
// the JSON response.
func (t *Transport) TSGetRetention(ctx context.Context, req *transport.TSGetRetentionRequest) (*transport.TSGetRetentionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, callErr := t.callTool(ctx, "qrdb_ts_get_retention_policy", map[string]any{
		"metric": req.Metric,
	})
	if callErr != nil {
		return nil, callErr
	}

	var raw serverRetentionPolicy
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "TSGetRetention", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.TSGetRetentionResponse{
		Policy: &transport.TSRetentionPolicy{
			DurationNs:   raw.Duration,
			ResolutionNs: raw.Resolution,
			Aggregation:  transport.AggFunc(raw.Aggregation),
		},
	}, nil
}

// TSSetRetention sets the retention policy for a metric. Calls the
// qrdb_ts_set_retention_policy MCP tool.
func (t *Transport) TSSetRetention(ctx context.Context, req *transport.TSSetRetentionRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	args := map[string]any{
		"metric":           req.Metric,
		"duration_seconds": req.Policy.DurationNs / int64(time.Second),
	}

	if req.Policy.ResolutionNs > 0 {
		args["resolution_seconds"] = req.Policy.ResolutionNs / int64(time.Second)
	}

	if req.Policy.Aggregation != transport.AggNone {
		funcName, ok := sdkAggFuncToString[req.Policy.Aggregation]
		if !ok {
			return &transport.RequestError{
				Method: "TSSetRetention",
				Err:    &toolResultError{Tool: "qrdb_ts_set_retention_policy", Message: "unsupported aggregation function"},
			}
		}
		args["aggregation"] = funcName
	}

	_, callErr := t.callTool(ctx, "qrdb_ts_set_retention_policy", args)
	return callErr
}

// TSListMetrics lists metric names matching a prefix. Calls the
// qrdb_ts_list_metrics MCP tool and parses the JSON array response.
// When req.Consistency is set, it is forwarded as the "consistency"
// tool argument.
func (t *Transport) TSListMetrics(ctx context.Context, req *transport.TSListMetricsRequest) (*transport.TSListMetricsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{}

	if req.Prefix != "" {
		args["prefix"] = req.Prefix
	}
	if req.Limit > 0 {
		args["limit"] = int64(req.Limit)
	}
	if req.Consistency != "" {
		args["consistency"] = req.Consistency
	}

	text, callErr := t.callTool(ctx, "qrdb_ts_list_metrics", args)
	if callErr != nil {
		return nil, callErr
	}

	var metrics []string
	if jsonErr := json.Unmarshal([]byte(text), &metrics); jsonErr != nil {
		return nil, &transport.RequestError{Method: "TSListMetrics", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.TSListMetricsResponse{Metrics: metrics}, nil
}

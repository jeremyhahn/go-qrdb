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

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Wire types for REST server communication
// ---------------------------------------------------------------------------

// tsWireDataPoint matches the JSON shape produced by the REST server for
// individual data points. The server uses map[string]string for tags and
// a "timestamp" field in nanoseconds, which differs from the SDK's
// []TSTag and "timestamp_ns" layout.
type tsWireDataPoint struct {
	Metric    string            `json:"metric"`
	Value     float64           `json:"value"`
	Timestamp int64             `json:"timestamp"`
	Tags      map[string]string `json:"tags,omitempty"`
}

// tsWireQueryResponse matches the JSON shape of GET /ts/query.
type tsWireQueryResponse struct {
	Points []tsWireDataPoint `json:"points"`
}

// tsWireAggResult matches the JSON shape of an individual aggregation result.
type tsWireAggResult struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
	Count     int64   `json:"count"`
}

// tsWireAggregateResponse matches the JSON shape of GET /ts/aggregate.
type tsWireAggregateResponse struct {
	Results []tsWireAggResult `json:"results"`
}

// tsWireWriteRequest matches the JSON shape of POST /ts/write.
type tsWireWriteRequest struct {
	Metric    string            `json:"metric"`
	Value     float64           `json:"value"`
	Timestamp int64             `json:"timestamp"`
	Tags      map[string]string `json:"tags,omitempty"`
}

// tsWireWriteBatchRequest matches the JSON shape of POST /ts/write-batch.
type tsWireWriteBatchRequest struct {
	Points []tsWireWriteRequest `json:"points"`
}

// tsWireRichQueryRequest matches the JSON shape of POST /ts/query-rich.
type tsWireRichQueryRequest struct {
	Metric      string            `json:"metric"`
	Tags        map[string]string `json:"tags,omitempty"`
	StartNs     int64             `json:"start_ns"`
	EndNs       int64             `json:"end_ns"`
	Aggregation string            `json:"aggregation,omitempty"`
	IntervalNs  int64             `json:"interval_ns,omitempty"`
	GroupBy     []string          `json:"group_by,omitempty"`
	Limit       int               `json:"limit,omitempty"`
	Percentile  float64           `json:"percentile,omitempty"`
}

// tsWireGroupedResult matches the JSON shape of a grouped query result.
type tsWireGroupedResult struct {
	Tags            map[string]string `json:"tags"`
	Points          []tsWireDataPoint `json:"points,omitempty"`
	AggregatedValue *float64          `json:"aggregated_value,omitempty"`
}

// tsWireRichQueryResponse matches the JSON shape of POST /ts/query-rich.
type tsWireRichQueryResponse struct {
	Metric          string                `json:"metric"`
	Tags            map[string]string     `json:"tags,omitempty"`
	Points          []tsWireDataPoint     `json:"points"`
	AggregatedValue *float64              `json:"aggregated_value,omitempty"`
	GroupedResults  []tsWireGroupedResult `json:"grouped_results,omitempty"`
}

// tsWireRetentionPolicy matches the JSON shape of a retention policy.
type tsWireRetentionPolicy struct {
	DurationNs   int64  `json:"duration_ns"`
	ResolutionNs int64  `json:"resolution_ns,omitempty"`
	Aggregation  string `json:"aggregation,omitempty"`
}

// tsWireGetRetentionResponse matches GET /ts/series/{metric}/retention.
type tsWireGetRetentionResponse struct {
	Metric string                 `json:"metric"`
	Policy *tsWireRetentionPolicy `json:"policy"`
}

// tsWireSetRetentionRequest matches PUT /ts/series/{metric}/retention.
type tsWireSetRetentionRequest struct {
	DurationNs   int64  `json:"duration_ns"`
	ResolutionNs int64  `json:"resolution_ns,omitempty"`
	Aggregation  string `json:"aggregation,omitempty"`
}

// tsWireListMetricsResponse matches GET /ts/metrics.
type tsWireListMetricsResponse struct {
	Metrics []string `json:"metrics"`
}

// ---------------------------------------------------------------------------
// TimeSeriesClient implementation
// ---------------------------------------------------------------------------

// TSWrite stores a single time-series data point. Maps to POST /ts/write.
func (t *Transport) TSWrite(ctx context.Context, point *transport.TSDataPoint) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := tsWireWriteRequest{
		Metric:    point.Metric,
		Value:     point.Value,
		Timestamp: point.TimestampNs,
		Tags:      tagsSliceToMap(point.Tags),
	}

	return t.postJSON(ctx, "TSWrite", "/ts/write", payload)
}

// TSWriteBatch stores multiple time-series data points atomically. Maps
// to POST /ts/write-batch.
func (t *Transport) TSWriteBatch(ctx context.Context, points []transport.TSDataPoint) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	wirePoints := make([]tsWireWriteRequest, len(points))
	for i, p := range points {
		wirePoints[i] = tsWireWriteRequest{
			Metric:    p.Metric,
			Value:     p.Value,
			Timestamp: p.TimestampNs,
			Tags:      tagsSliceToMap(p.Tags),
		}
	}

	return t.postJSON(ctx, "TSWriteBatch", "/ts/write-batch", tsWireWriteBatchRequest{
		Points: wirePoints,
	})
}

// TSQuery retrieves data points for a metric within a time range. Maps to
// GET /ts/query with query parameters.
func (t *Transport) TSQuery(ctx context.Context, req *transport.TSQueryRequest) (*transport.TSQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	params.Set("metric", req.Metric)
	params.Set("start_ns", strconv.FormatInt(req.StartNs, 10))
	params.Set("end_ns", strconv.FormatInt(req.EndNs, 10))

	if len(req.Tags) > 0 {
		params.Set("tags", encodeTagsQuery(req.Tags))
	}
	if req.Limit > 0 {
		params.Set("limit", strconv.FormatInt(int64(req.Limit), 10))
	}
	if req.Descending {
		params.Set("descending", "true")
	}
	if req.Consistency != "" {
		params.Set("consistency", req.Consistency)
	}

	reqURL := t.baseURL + "/ts/query?" + params.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "TSQuery", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TSQuery", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("TSQuery", resp)
	}

	var wireResp tsWireQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "TSQuery", Err: &jsonDecodeError{Err: err}}
	}

	sdkPoints := make([]transport.TSDataPoint, len(wireResp.Points))
	for i, wp := range wireResp.Points {
		sdkPoints[i] = wirePointToSDK(wp)
	}

	return &transport.TSQueryResponse{Points: sdkPoints}, nil
}

// TSAggregate computes an aggregation over data points in a time range.
// Maps to GET /ts/aggregate with query parameters.
func (t *Transport) TSAggregate(ctx context.Context, req *transport.TSAggregateRequest) (*transport.TSAggregateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	params.Set("metric", req.Metric)
	params.Set("start_ns", strconv.FormatInt(req.StartNs, 10))
	params.Set("end_ns", strconv.FormatInt(req.EndNs, 10))
	params.Set("function", aggFuncToString(req.Function))
	if req.IntervalNs > 0 {
		params.Set("interval_ns", strconv.FormatInt(req.IntervalNs, 10))
	}
	if req.Consistency != "" {
		params.Set("consistency", req.Consistency)
	}

	reqURL := t.baseURL + "/ts/aggregate?" + params.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "TSAggregate", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TSAggregate", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("TSAggregate", resp)
	}

	var wireResp tsWireAggregateResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "TSAggregate", Err: &jsonDecodeError{Err: err}}
	}

	results := make([]transport.TSAggResult, len(wireResp.Results))
	for i, wr := range wireResp.Results {
		results[i] = transport.TSAggResult{
			TimestampNs: wr.Timestamp,
			Value:       wr.Value,
			Count:       wr.Count,
		}
	}

	return &transport.TSAggregateResponse{Results: results}, nil
}

// TSWritePoint stores a single validated data point. Maps to
// POST /ts/write-point.
func (t *Transport) TSWritePoint(ctx context.Context, point *transport.TSDataPoint) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := tsWireWriteRequest{
		Metric:    point.Metric,
		Value:     point.Value,
		Timestamp: point.TimestampNs,
		Tags:      tagsSliceToMap(point.Tags),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return &transport.RequestError{Method: "TSWritePoint", Err: &jsonEncodeError{Err: err}}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/ts/write-point", bytes.NewReader(data))
	if err != nil {
		return &transport.RequestError{Method: "TSWritePoint", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "TSWritePoint", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}

	return t.decodeErrorResponse("TSWritePoint", resp)
}

// TSRichQuery performs a rich query with aggregation, downsampling, and
// grouping. Maps to POST /ts/query-rich.
func (t *Transport) TSRichQuery(ctx context.Context, req *transport.TSRichQueryRequest) (*transport.TSRichQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	wireReq := tsWireRichQueryRequest{
		Metric:      req.Metric,
		Tags:        tagsSliceToMap(req.Tags),
		StartNs:     req.StartNs,
		EndNs:       req.EndNs,
		Aggregation: aggFuncToString(req.Aggregation),
		IntervalNs:  req.IntervalNs,
		GroupBy:     req.GroupBy,
		Limit:       req.Limit,
		Percentile:  req.Percentile,
	}

	// Omit the aggregation field when it is the zero value (AggNone).
	if req.Aggregation == transport.AggNone {
		wireReq.Aggregation = ""
	}

	data, err := json.Marshal(wireReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TSRichQuery", Err: &jsonEncodeError{Err: err}}
	}

	richQueryURL := t.baseURL + "/ts/query-rich"
	if req.Consistency != "" {
		params := url.Values{}
		params.Set("consistency", req.Consistency)
		richQueryURL += "?" + params.Encode()
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, richQueryURL, bytes.NewReader(data))
	if err != nil {
		return nil, &transport.RequestError{Method: "TSRichQuery", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TSRichQuery", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("TSRichQuery", resp)
	}

	var wireResp tsWireRichQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "TSRichQuery", Err: &jsonDecodeError{Err: err}}
	}

	sdkResp := &transport.TSRichQueryResponse{
		Metric:          wireResp.Metric,
		Tags:            tagsMapToSlice(wireResp.Tags),
		AggregatedValue: wireResp.AggregatedValue,
	}

	sdkResp.Points = make([]transport.TSDataPoint, len(wireResp.Points))
	for i, wp := range wireResp.Points {
		sdkResp.Points[i] = wirePointToSDK(wp)
	}

	if len(wireResp.GroupedResults) > 0 {
		sdkResp.GroupedResults = make([]transport.TSGroupedResult, len(wireResp.GroupedResults))
		for i, gr := range wireResp.GroupedResults {
			pts := make([]transport.TSDataPoint, len(gr.Points))
			for j, wp := range gr.Points {
				pts[j] = wirePointToSDK(wp)
			}
			sdkResp.GroupedResults[i] = transport.TSGroupedResult{
				Tags:            tagsMapToSlice(gr.Tags),
				Points:          pts,
				AggregatedValue: gr.AggregatedValue,
			}
		}
	}

	return sdkResp, nil
}

// TSDeleteSeries deletes time-series data matching the criteria. Maps to
// DELETE /ts/series/{metric} with optional query parameters.
func (t *Transport) TSDeleteSeries(ctx context.Context, req *transport.TSDeleteRequest) (*transport.TSDeleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/ts/series/" + url.PathEscape(req.Metric)

	params := url.Values{}
	if len(req.Tags) > 0 {
		params.Set("tags", encodeTagsQuery(req.Tags))
	}
	if req.StartNs > 0 {
		params.Set("start_ns", strconv.FormatInt(req.StartNs, 10))
	}
	if req.EndNs > 0 {
		params.Set("end_ns", strconv.FormatInt(req.EndNs, 10))
	}
	if encoded := params.Encode(); encoded != "" {
		reqURL += "?" + encoded
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "TSDeleteSeries", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TSDeleteSeries", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("TSDeleteSeries", resp)
	}

	return &transport.TSDeleteResponse{}, nil
}

// TSGetRetention retrieves the retention policy for a metric. Maps to
// GET /ts/series/{metric}/retention.
func (t *Transport) TSGetRetention(ctx context.Context, req *transport.TSGetRetentionRequest) (*transport.TSGetRetentionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/ts/series/" + url.PathEscape(req.Metric) + "/retention"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "TSGetRetention", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TSGetRetention", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("TSGetRetention", resp)
	}

	var wireResp tsWireGetRetentionResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "TSGetRetention", Err: &jsonDecodeError{Err: err}}
	}

	sdkResp := &transport.TSGetRetentionResponse{}
	if wireResp.Policy != nil {
		sdkResp.Policy = &transport.TSRetentionPolicy{
			DurationNs:   wireResp.Policy.DurationNs,
			ResolutionNs: wireResp.Policy.ResolutionNs,
			Aggregation:  stringToAggFunc(wireResp.Policy.Aggregation),
		}
	}

	return sdkResp, nil
}

// TSSetRetention sets the retention policy for a metric. Maps to
// PUT /ts/series/{metric}/retention.
func (t *Transport) TSSetRetention(ctx context.Context, req *transport.TSSetRetentionRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	wireReq := tsWireSetRetentionRequest{
		DurationNs:   req.Policy.DurationNs,
		ResolutionNs: req.Policy.ResolutionNs,
		Aggregation:  aggFuncToString(req.Policy.Aggregation),
	}

	// Omit the aggregation field when it is the zero value (AggNone).
	if req.Policy.Aggregation == transport.AggNone {
		wireReq.Aggregation = ""
	}

	data, err := json.Marshal(wireReq)
	if err != nil {
		return &transport.RequestError{Method: "TSSetRetention", Err: &jsonEncodeError{Err: err}}
	}

	reqURL := t.baseURL + "/ts/series/" + url.PathEscape(req.Metric) + "/retention"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(data))
	if err != nil {
		return &transport.RequestError{Method: "TSSetRetention", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "TSSetRetention", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return t.decodeErrorResponse("TSSetRetention", resp)
	}

	return nil
}

// TSListMetrics lists metric names matching a prefix. Maps to
// GET /ts/metrics with optional query parameters.
func (t *Transport) TSListMetrics(ctx context.Context, req *transport.TSListMetricsRequest) (*transport.TSListMetricsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	if req.Prefix != "" {
		params.Set("prefix", req.Prefix)
	}
	if req.Limit > 0 {
		params.Set("limit", strconv.FormatInt(int64(req.Limit), 10))
	}
	if req.Consistency != "" {
		params.Set("consistency", req.Consistency)
	}

	reqURL := t.baseURL + "/ts/metrics"
	if encoded := params.Encode(); encoded != "" {
		reqURL += "?" + encoded
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "TSListMetrics", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TSListMetrics", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("TSListMetrics", resp)
	}

	var wireResp tsWireListMetricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "TSListMetrics", Err: &jsonDecodeError{Err: err}}
	}

	return &transport.TSListMetricsResponse{Metrics: wireResp.Metrics}, nil
}

// ---------------------------------------------------------------------------
// Tag conversion helpers
// ---------------------------------------------------------------------------

// tagsSliceToMap converts an SDK []TSTag to the map[string]string used by
// the REST API server.
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

// tagsMapToSlice converts a map[string]string from the REST API server to
// the SDK []TSTag representation.
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

// encodeTagsQuery encodes an SDK []TSTag into the comma-separated
// key=value query parameter format expected by the REST server.
func encodeTagsQuery(tags []transport.TSTag) string {
	parts := make([]string, len(tags))
	for i, t := range tags {
		parts[i] = t.Key + "=" + t.Value
	}
	return strings.Join(parts, ",")
}

// wirePointToSDK converts a wire data point to the SDK transport type.
func wirePointToSDK(wp tsWireDataPoint) transport.TSDataPoint {
	return transport.TSDataPoint{
		Metric:      wp.Metric,
		Value:       wp.Value,
		TimestampNs: wp.Timestamp,
		Tags:        tagsMapToSlice(wp.Tags),
	}
}

// ---------------------------------------------------------------------------
// AggFunc string conversion helpers
// ---------------------------------------------------------------------------

// aggFuncStringMap maps AggFunc enum values to the string names expected
// by the REST server.
var aggFuncStringMap = map[transport.AggFunc]string{
	transport.AggNone:       "",
	transport.AggAvg:        "avg",
	transport.AggSum:        "sum",
	transport.AggMin:        "min",
	transport.AggMax:        "max",
	transport.AggCount:      "count",
	transport.AggPercentile: "percentile",
	transport.AggFirst:      "first",
	transport.AggLast:       "last",
}

// stringAggFuncMap maps REST server aggregation function name strings back
// to AggFunc enum values.
var stringAggFuncMap = map[string]transport.AggFunc{
	"":           transport.AggNone,
	"avg":        transport.AggAvg,
	"sum":        transport.AggSum,
	"min":        transport.AggMin,
	"max":        transport.AggMax,
	"count":      transport.AggCount,
	"percentile": transport.AggPercentile,
	"first":      transport.AggFirst,
	"last":       transport.AggLast,
}

// aggFuncToString converts an AggFunc enum to the string expected by
// the REST server.
func aggFuncToString(fn transport.AggFunc) string {
	if s, ok := aggFuncStringMap[fn]; ok {
		return s
	}
	return ""
}

// stringToAggFunc converts a REST server aggregation function name to
// the SDK AggFunc enum.
func stringToAggFunc(s string) transport.AggFunc {
	if fn, ok := stringAggFuncMap[s]; ok {
		return fn
	}
	return transport.AggNone
}

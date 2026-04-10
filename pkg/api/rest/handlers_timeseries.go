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
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// TimeSeries request/response types
// ---------------------------------------------------------------------------

// tsWriteRequest is the JSON payload for POST /ts/write.
type tsWriteRequest struct {
	Metric    string            `json:"metric"`
	Value     float64           `json:"value"`
	Timestamp int64             `json:"timestamp"` // nanoseconds since epoch
	Tags      map[string]string `json:"tags,omitempty"`
}

// tsWriteBatchRequest is the JSON payload for POST /ts/write-batch.
type tsWriteBatchRequest struct {
	Points []tsWriteRequest `json:"points"`
}

// tsDataPointJSON is the JSON representation of a time-series data point.
type tsDataPointJSON struct {
	Metric    string            `json:"metric"`
	Value     float64           `json:"value"`
	Timestamp int64             `json:"timestamp"` // nanoseconds since epoch
	Tags      map[string]string `json:"tags,omitempty"`
}

// tsQueryResponse is the JSON payload returned by GET /ts/query.
type tsQueryResponse struct {
	Points []tsDataPointJSON `json:"points"`
}

// tsAggResultJSON is the JSON representation of an aggregation result.
type tsAggResultJSON struct {
	Timestamp int64   `json:"timestamp"` // nanoseconds since epoch
	Value     float64 `json:"value"`
	Count     int64   `json:"count"`
}

// tsAggregateResponse is the JSON payload returned by GET /ts/aggregate.
type tsAggregateResponse struct {
	Results []tsAggResultJSON `json:"results"`
}

// tsDeleteSeriesResponse is the JSON payload returned by DELETE /ts/series/{metric}.
type tsDeleteSeriesResponse struct {
	Status string `json:"status"`
}

// tsRetentionPolicyJSON is the JSON representation of a retention policy.
type tsRetentionPolicyJSON struct {
	DurationNs   int64  `json:"duration_ns"`
	ResolutionNs int64  `json:"resolution_ns,omitempty"`
	Aggregation  string `json:"aggregation,omitempty"`
}

// tsGetRetentionResponse is the JSON payload returned by GET /ts/series/{metric}/retention.
type tsGetRetentionResponse struct {
	Metric string                 `json:"metric"`
	Policy *tsRetentionPolicyJSON `json:"policy"`
}

// tsSetRetentionRequest is the JSON payload for PUT /ts/series/{metric}/retention.
type tsSetRetentionRequest struct {
	DurationNs   int64  `json:"duration_ns"`
	ResolutionNs int64  `json:"resolution_ns,omitempty"`
	Aggregation  string `json:"aggregation,omitempty"`
}

// tsSetRetentionResponse is the JSON payload returned by PUT /ts/series/{metric}/retention.
type tsSetRetentionResponse struct {
	Status string `json:"status"`
}

// tsListMetricsResponse is the JSON payload returned by GET /ts/metrics.
type tsListMetricsResponse struct {
	Metrics []string `json:"metrics"`
}

// ---------------------------------------------------------------------------
// TimeSeries handlers
// ---------------------------------------------------------------------------

// handleTSWrite handles POST /ts/write -- stores a single time-series data
// point through Raft consensus.
func (s *Server) handleTSWrite(w http.ResponseWriter, r *http.Request) {
	if s.tsSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "timeseries"})
		return
	}

	var req tsWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	timestamp := time.Unix(0, req.Timestamp)

	if err := s.tsSvc.Write(r.Context(), req.Metric, req.Value, timestamp, req.Tags); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// handleTSWriteBatch handles POST /ts/write-batch -- stores multiple
// time-series data points atomically through Raft consensus.
func (s *Server) handleTSWriteBatch(w http.ResponseWriter, r *http.Request) {
	if s.tsSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "timeseries"})
		return
	}

	var req tsWriteBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	points := make([]timeseries.DataPoint, len(req.Points))
	for i, p := range req.Points {
		points[i] = timeseries.DataPoint{
			Metric:    p.Metric,
			Value:     p.Value,
			Timestamp: time.Unix(0, p.Timestamp),
			Tags:      p.Tags,
		}
	}

	if err := s.tsSvc.WriteBatch(r.Context(), points); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// handleTSQuery handles GET /ts/query -- retrieves time-series data points
// for a metric within the given time range.
// Query parameters:
//   - metric: metric name (required)
//   - start_ns: start time in nanoseconds since epoch (required)
//   - end_ns: end time in nanoseconds since epoch (required)
//   - tags: comma-separated key=value pairs for filtering (optional)
//   - limit: maximum number of data points (optional)
//   - descending: reverse chronological order when "true" (optional)
func (s *Server) handleTSQuery(w http.ResponseWriter, r *http.Request) {
	if s.tsSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "timeseries"})
		return
	}

	q := r.URL.Query()

	metric := q.Get("metric")
	if metric == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "metric"})
		return
	}

	startNs, err := strconv.ParseInt(q.Get("start_ns"), 10, 64)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "start_ns", Err: err})
		return
	}

	endNs, err := strconv.ParseInt(q.Get("end_ns"), 10, 64)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "end_ns", Err: err})
		return
	}

	start := time.Unix(0, startNs)
	end := time.Unix(0, endNs)

	var opts []timeseries.TSQueryOption

	if tagsStr := q.Get("tags"); tagsStr != "" {
		tags := parseTags(tagsStr)
		if len(tags) > 0 {
			opts = append(opts, timeseries.WithTags(tags))
		}
	}

	if limitStr := q.Get("limit"); limitStr != "" {
		limit, parseErr := strconv.Atoi(limitStr)
		if parseErr != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "limit", Err: parseErr})
			return
		}
		opts = append(opts, timeseries.WithTSLimit(limit))
	}

	if q.Get("descending") == "true" {
		opts = append(opts, timeseries.WithDescending())
	}

	points, queryErr := s.tsSvc.Query(r.Context(), metric, start, end, opts...)
	if queryErr != nil {
		writeErrorJSON(w, mapHTTPError(queryErr), queryErr)
		return
	}

	resp := make([]tsDataPointJSON, len(points))
	for i, p := range points {
		resp[i] = tsDataPointJSON{
			Metric:    p.Metric,
			Value:     p.Value,
			Timestamp: p.Timestamp.UnixNano(),
			Tags:      p.Tags,
		}
	}

	writeJSON(w, tsQueryResponse{Points: resp})
}

// handleTSAggregate handles GET /ts/aggregate -- computes an aggregation
// function over time-series data points.
// Query parameters:
//   - metric: metric name (required)
//   - start_ns: start time in nanoseconds since epoch (required)
//   - end_ns: end time in nanoseconds since epoch (required)
//   - function: aggregation function name (required: avg, sum, min, max, count)
//   - interval_ns: aggregation interval in nanoseconds (required)
func (s *Server) handleTSAggregate(w http.ResponseWriter, r *http.Request) {
	if s.tsSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "timeseries"})
		return
	}

	q := r.URL.Query()

	metric := q.Get("metric")
	if metric == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "metric"})
		return
	}

	startNs, err := strconv.ParseInt(q.Get("start_ns"), 10, 64)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "start_ns", Err: err})
		return
	}

	endNs, err := strconv.ParseInt(q.Get("end_ns"), 10, 64)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "end_ns", Err: err})
		return
	}

	fnStr := q.Get("function")
	if fnStr == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "function"})
		return
	}

	fn, ok := aggFuncMap[fnStr]
	if !ok {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{
			Param: "function",
			Err:   &InvalidAggFuncError{Func: fnStr},
		})
		return
	}

	var intervalNs int64
	if intervalStr := q.Get("interval_ns"); intervalStr != "" {
		intervalNs, err = strconv.ParseInt(intervalStr, 10, 64)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "interval_ns", Err: err})
			return
		}
	}

	start := time.Unix(0, startNs)
	end := time.Unix(0, endNs)
	interval := time.Duration(intervalNs)

	results, aggErr := s.tsSvc.Aggregate(r.Context(), metric, start, end, fn, interval)
	if aggErr != nil {
		writeErrorJSON(w, mapHTTPError(aggErr), aggErr)
		return
	}

	resp := make([]tsAggResultJSON, len(results))
	for i, ar := range results {
		resp[i] = tsAggResultJSON{
			Timestamp: ar.Timestamp.UnixNano(),
			Value:     ar.Value,
			Count:     ar.Count,
		}
	}

	writeJSON(w, tsAggregateResponse{Results: resp})
}

// ---------------------------------------------------------------------------
// TimeSeries series handlers (path-parameter based)
// ---------------------------------------------------------------------------

// handleTSSeries routes requests under the /ts/series/ prefix. It extracts
// the metric name from the URL path and dispatches to the appropriate handler
// based on the HTTP method and suffix.
//
// Supported routes:
//   - DELETE /ts/series/{metric}            -- delete series data
//   - GET    /ts/series/{metric}/retention  -- get retention policy
//   - PUT    /ts/series/{metric}/retention  -- set retention policy
func (s *Server) handleTSSeries(w http.ResponseWriter, r *http.Request) {
	if s.tsSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "timeseries"})
		return
	}

	// Extract path after "/ts/series/".
	rest := strings.TrimPrefix(r.URL.Path, "/ts/series/")
	if rest == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "metric"})
		return
	}

	// Check for /retention suffix.
	if metric, found := strings.CutSuffix(rest, "/retention"); found && metric != "" {
		switch r.Method {
		case http.MethodGet:
			s.handleTSGetRetention(w, r, metric)
		case http.MethodPut:
			s.handleTSSetRetention(w, r, metric)
		default:
			w.Header().Set("Allow", "GET, PUT")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// No suffix: the path is the metric name for DELETE.
	metric := rest
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.handleTSDeleteSeries(w, r, metric)
}

// handleTSDeleteSeries handles DELETE /ts/series/{metric} -- deletes
// time-series data for the given metric.
// Query parameters:
//   - start_ns: start time in nanoseconds since epoch (optional)
//   - end_ns: end time in nanoseconds since epoch (optional)
//   - tags: comma-separated key=value pairs for filtering (optional)
func (s *Server) handleTSDeleteSeries(w http.ResponseWriter, r *http.Request, metric string) {
	q := r.URL.Query()

	req := &timeseries.DeleteRequest{
		Metric: metric,
	}

	if tagsStr := q.Get("tags"); tagsStr != "" {
		req.Tags = parseTags(tagsStr)
	}

	if startStr := q.Get("start_ns"); startStr != "" {
		startNs, err := strconv.ParseInt(startStr, 10, 64)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "start_ns", Err: err})
			return
		}
		req.StartTime = time.Unix(0, startNs)
	}

	if endStr := q.Get("end_ns"); endStr != "" {
		endNs, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "end_ns", Err: err})
			return
		}
		req.EndTime = time.Unix(0, endNs)
	}

	if err := s.tsSvc.DeleteSeries(r.Context(), req); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, tsDeleteSeriesResponse{Status: "ok"})
}

// handleTSGetRetention handles GET /ts/series/{metric}/retention --
// retrieves the retention policy for the given metric.
func (s *Server) handleTSGetRetention(w http.ResponseWriter, r *http.Request, metric string) {
	policy, err := s.tsSvc.GetRetentionPolicy(metric)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	var policyJSON *tsRetentionPolicyJSON
	if policy != nil {
		policyJSON = &tsRetentionPolicyJSON{
			DurationNs:   int64(policy.Duration),
			ResolutionNs: int64(policy.Resolution),
			Aggregation:  policy.Aggregation.String(),
		}
	}

	writeJSON(w, tsGetRetentionResponse{
		Metric: metric,
		Policy: policyJSON,
	})
}

// handleTSSetRetention handles PUT /ts/series/{metric}/retention --
// sets the retention policy for the given metric.
func (s *Server) handleTSSetRetention(w http.ResponseWriter, r *http.Request, metric string) {
	var req tsSetRetentionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if req.DurationNs <= 0 {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{
			Param: "duration_ns",
			Err:   &InvalidRetentionDurationError{},
		})
		return
	}

	agg := timeseries.AggNone
	if req.Aggregation != "" {
		var ok bool
		agg, ok = aggFuncMap[req.Aggregation]
		if !ok {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{
				Param: "aggregation",
				Err:   &InvalidAggFuncError{Func: req.Aggregation},
			})
			return
		}
	}

	policy := &timeseries.RetentionPolicy{
		Duration:    time.Duration(req.DurationNs),
		Resolution:  time.Duration(req.ResolutionNs),
		Aggregation: agg,
	}

	if err := s.tsSvc.SetRetentionPolicy(metric, policy); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, tsSetRetentionResponse{Status: "ok"})
}

// handleTSListMetrics handles GET /ts/metrics -- lists available metric
// names with optional prefix filtering.
// Query parameters:
//   - prefix: metric name prefix filter (optional)
//   - limit: maximum number of metrics to return (optional, default 100)
func (s *Server) handleTSListMetrics(w http.ResponseWriter, r *http.Request) {
	if s.tsSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "timeseries"})
		return
	}

	q := r.URL.Query()
	prefix := q.Get("prefix")

	limit := 100
	if limitStr := q.Get("limit"); limitStr != "" {
		parsed, err := strconv.Atoi(limitStr)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "limit", Err: err})
			return
		}
		if parsed > 0 {
			limit = parsed
		}
	}

	metrics, err := s.tsSvc.ListMetrics(prefix, limit)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	if metrics == nil {
		metrics = []string{}
	}

	writeJSON(w, tsListMetricsResponse{Metrics: metrics})
}

// ---------------------------------------------------------------------------
// TimeSeries WritePoint / RichQuery handlers
// ---------------------------------------------------------------------------

// tsWritePointRequest is the JSON payload for POST /ts/write-point.
type tsWritePointRequest struct {
	Metric    string            `json:"metric"`
	Value     float64           `json:"value"`
	Timestamp int64             `json:"timestamp"` // nanoseconds since epoch
	Tags      map[string]string `json:"tags,omitempty"`
}

// tsRichQueryRequest is the JSON payload for POST /ts/query-rich.
type tsRichQueryRequest struct {
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

// tsRichQueryResponse is the JSON payload returned by POST /ts/query-rich.
type tsRichQueryResponse struct {
	Metric          string                `json:"metric"`
	Tags            map[string]string     `json:"tags,omitempty"`
	Points          []tsDataPointJSON     `json:"points"`
	AggregatedValue *float64              `json:"aggregated_value,omitempty"`
	GroupedResults  []tsGroupedResultJSON `json:"grouped_results,omitempty"`
}

// tsGroupedResultJSON is the JSON representation of a grouped query result.
type tsGroupedResultJSON struct {
	Tags            map[string]string `json:"tags"`
	Points          []tsDataPointJSON `json:"points,omitempty"`
	AggregatedValue *float64          `json:"aggregated_value,omitempty"`
}

// handleTSWritePoint handles POST /ts/write-point -- stores a single
// validated data point through Raft consensus using the WritePoint service
// method.
func (s *Server) handleTSWritePoint(w http.ResponseWriter, r *http.Request) {
	if s.tsSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "timeseries"})
		return
	}

	var req tsWritePointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	point := &timeseries.DataPoint{
		Metric:    req.Metric,
		Value:     req.Value,
		Timestamp: time.Unix(0, req.Timestamp),
		Tags:      req.Tags,
	}

	if err := s.tsSvc.WritePoint(r.Context(), point); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleTSRichQuery handles POST /ts/query-rich -- performs a rich query
// with filtering, aggregation, downsampling, and grouping through the
// service layer.
func (s *Server) handleTSRichQuery(w http.ResponseWriter, r *http.Request) {
	if s.tsSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "timeseries"})
		return
	}

	var req tsRichQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if req.Metric == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "metric"})
		return
	}

	qr := &timeseries.QueryRequest{
		Metric:     req.Metric,
		Tags:       req.Tags,
		StartTime:  time.Unix(0, req.StartNs),
		EndTime:    time.Unix(0, req.EndNs),
		Interval:   time.Duration(req.IntervalNs),
		GroupBy:    req.GroupBy,
		Limit:      req.Limit,
		Percentile: req.Percentile,
	}

	if req.Aggregation != "" {
		agg, ok := aggFuncMap[req.Aggregation]
		if !ok {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{
				Param: "aggregation",
				Err:   &InvalidAggFuncError{Func: req.Aggregation},
			})
			return
		}
		qr.Aggregation = agg
	}

	result, err := s.tsSvc.QueryRich(r.Context(), qr)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	resp := tsRichQueryResponse{
		Metric:          result.Metric,
		Tags:            result.Tags,
		AggregatedValue: result.AggregatedValue,
	}

	resp.Points = make([]tsDataPointJSON, 0, len(result.Points))
	for _, p := range result.Points {
		resp.Points = append(resp.Points, tsDataPointJSON{
			Metric:    p.Metric,
			Value:     p.Value,
			Timestamp: p.Timestamp.UnixNano(),
			Tags:      p.Tags,
		})
	}

	if len(result.GroupedResults) > 0 {
		resp.GroupedResults = make([]tsGroupedResultJSON, len(result.GroupedResults))
		for i, gr := range result.GroupedResults {
			pts := make([]tsDataPointJSON, 0, len(gr.Points))
			for _, p := range gr.Points {
				pts = append(pts, tsDataPointJSON{
					Metric:    p.Metric,
					Value:     p.Value,
					Timestamp: p.Timestamp.UnixNano(),
					Tags:      p.Tags,
				})
			}
			resp.GroupedResults[i] = tsGroupedResultJSON{
				Tags:            gr.Tags,
				Points:          pts,
				AggregatedValue: gr.AggregatedValue,
			}
		}
	}

	writeJSON(w, resp)
}

// ---------------------------------------------------------------------------
// TimeSeries helpers
// ---------------------------------------------------------------------------

// aggFuncMap maps aggregation function name strings to AggFunc constants.
var aggFuncMap = map[string]timeseries.AggFunc{
	"avg":        timeseries.AggAvg,
	"sum":        timeseries.AggSum,
	"min":        timeseries.AggMin,
	"max":        timeseries.AggMax,
	"count":      timeseries.AggCount,
	"percentile": timeseries.AggPercentile,
}

// parseTags parses a comma-separated list of key=value pairs into a map.
// Example: "region=us-east,env=prod" -> {"region": "us-east", "env": "prod"}
func parseTags(s string) map[string]string {
	tags := make(map[string]string)
	pairs := strings.Split(s, ",")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			key := strings.TrimSpace(kv[0])
			val := strings.TrimSpace(kv[1])
			if key != "" {
				tags[key] = val
			}
		}
	}
	return tags
}

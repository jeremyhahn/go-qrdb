//go:build qrdb_ts

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var aggFuncMap = map[string]transport.AggFunc{
	"avg":        transport.AggAvg,
	"sum":        transport.AggSum,
	"min":        transport.AggMin,
	"max":        transport.AggMax,
	"count":      transport.AggCount,
	"percentile": transport.AggPercentile,
}

func newTSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ts",
		Short: "Time-series data operations",
	}
	cmd.AddCommand(
		newTSWriteCmd(),
		newTSWriteBatchCmd(),
		newTSQueryCmd(),
		newTSAggregateCmd(),
	)
	return cmd
}

func newTSWriteCmd() *cobra.Command {
	var (
		metric    string
		value     float64
		timestamp int64
		tags      []string
	)
	cmd := &cobra.Command{
		Use:   "write",
		Short: "Write a time-series data point",
		RunE: func(cmd *cobra.Command, args []string) error {
			if timestamp == 0 {
				timestamp = time.Now().UnixNano()
			}
			point := &transport.TSDataPoint{
				Metric:      metric,
				Value:       value,
				TimestampNs: timestamp,
				Tags:        parseTags(tags),
			}
			if err := client.TimeSeries().TSWrite(cmd.Context(), point); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}
	cmd.Flags().StringVar(&metric, "metric", "", "Metric name")
	cmd.Flags().Float64Var(&value, "value", 0, "Data point value")
	cmd.Flags().Int64Var(&timestamp, "timestamp", 0, "Nanosecond timestamp (default: current time)")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "Tags in k=v format (repeatable)")
	_ = cmd.MarkFlagRequired("metric")
	_ = cmd.MarkFlagRequired("value")
	return cmd
}

func newTSWriteBatchCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "write-batch",
		Short: "Write a batch of time-series data points from JSON",
		RunE: func(cmd *cobra.Command, args []string) error {
			var data []byte
			var err error
			if file == "-" {
				data, err = io.ReadAll(os.Stdin)
				if err != nil {
					return &FileReadError{Path: file, Err: err}
				}
			} else {
				data, err = os.ReadFile(file)
				if err != nil {
					return &FileReadError{Path: file, Err: err}
				}
			}
			var points []transport.TSDataPoint
			if err := json.Unmarshal(data, &points); err != nil {
				return err
			}
			if err := client.TimeSeries().TSWriteBatch(cmd.Context(), points); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "JSON file path (or \"-\" for stdin)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func newTSQueryCmd() *cobra.Command {
	var (
		metric     string
		startNs    int64
		endNs      int64
		tags       []string
		limit      int32
		descending bool
	)
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Query time-series data points",
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &transport.TSQueryRequest{
				Metric:     metric,
				StartNs:    startNs,
				EndNs:      endNs,
				Tags:       parseTags(tags),
				Limit:      limit,
				Descending: descending,
			}
			resp, err := client.TimeSeries().TSQuery(cmd.Context(), req)
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"TIMESTAMP_NS", "METRIC", "VALUE", "TAGS"}
				rows := make([][]string, 0, len(resp.Points))
				for _, p := range resp.Points {
					tagPairs := make([]string, 0, len(p.Tags))
					for _, t := range p.Tags {
						tagPairs = append(tagPairs, t.Key+"="+t.Value)
					}
					rows = append(rows, []string{
						strconv.FormatInt(p.TimestampNs, 10),
						p.Metric,
						fmt.Sprintf("%g", p.Value),
						strings.Join(tagPairs, ","),
					})
				}
				printTable(headers, rows)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&metric, "metric", "", "Metric name")
	cmd.Flags().Int64Var(&startNs, "start", 0, "Start timestamp in nanoseconds")
	cmd.Flags().Int64Var(&endNs, "end", 0, "End timestamp in nanoseconds")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "Filter tags in k=v format (repeatable)")
	cmd.Flags().Int32Var(&limit, "limit", 0, "Maximum number of results")
	cmd.Flags().BoolVar(&descending, "desc", false, "Descending order")
	_ = cmd.MarkFlagRequired("metric")
	_ = cmd.MarkFlagRequired("start")
	_ = cmd.MarkFlagRequired("end")
	return cmd
}

func newTSAggregateCmd() *cobra.Command {
	var (
		metric     string
		startNs    int64
		endNs      int64
		function   string
		intervalNs int64
	)
	cmd := &cobra.Command{
		Use:   "aggregate",
		Short: "Aggregate time-series data",
		RunE: func(cmd *cobra.Command, args []string) error {
			fn, ok := aggFuncMap[strings.ToLower(function)]
			if !ok {
				return &InvalidAggFuncError{Input: function}
			}
			req := &transport.TSAggregateRequest{
				Metric:     metric,
				StartNs:    startNs,
				EndNs:      endNs,
				Function:   fn,
				IntervalNs: intervalNs,
			}
			resp, err := client.TimeSeries().TSAggregate(cmd.Context(), req)
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"TIMESTAMP_NS", "VALUE", "COUNT"}
				rows := make([][]string, 0, len(resp.Results))
				for _, r := range resp.Results {
					rows = append(rows, []string{
						strconv.FormatInt(r.TimestampNs, 10),
						fmt.Sprintf("%g", r.Value),
						strconv.FormatInt(r.Count, 10),
					})
				}
				printTable(headers, rows)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&metric, "metric", "", "Metric name")
	cmd.Flags().Int64Var(&startNs, "start", 0, "Start timestamp in nanoseconds")
	cmd.Flags().Int64Var(&endNs, "end", 0, "End timestamp in nanoseconds")
	cmd.Flags().StringVar(&function, "function", "", "Aggregation function (avg, sum, min, max, count, percentile)")
	cmd.Flags().Int64Var(&intervalNs, "interval", 0, "Interval in nanoseconds")
	_ = cmd.MarkFlagRequired("metric")
	_ = cmd.MarkFlagRequired("start")
	_ = cmd.MarkFlagRequired("end")
	_ = cmd.MarkFlagRequired("function")
	return cmd
}

func parseTags(tags []string) []transport.TSTag {
	if len(tags) == 0 {
		return nil
	}
	parsed := make([]transport.TSTag, 0, len(tags))
	for _, t := range tags {
		parts := strings.SplitN(t, "=", 2)
		if len(parts) == 2 {
			parsed = append(parsed, transport.TSTag{
				Key:   parts[0],
				Value: parts[1],
			})
		}
	}
	return parsed
}

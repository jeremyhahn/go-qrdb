// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package vector

import (
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// vectorMetrics holds Prometheus metrics for vector storage operations.
type vectorMetrics struct {
	// Operation counters
	upsertOps     *metrics.Counter
	getOps        *metrics.Counter
	deleteOps     *metrics.Counter
	scanOps       *metrics.Counter
	batchCommits  *metrics.Counter
	namespaceOps  *metrics.Counter
	idAllocations *metrics.Counter

	// Latency histograms
	upsertLatency *metrics.Histogram
	getLatency    *metrics.Histogram
	deleteLatency *metrics.Histogram
	scanLatency   *metrics.Histogram
	batchLatency  *metrics.Histogram

	// Resource metrics
	bytesRead       *metrics.Counter
	bytesWritten    *metrics.Counter
	vectorsStored   *metrics.Gauge
	namespacesCount *metrics.Gauge

	// Error counter
	errors *metrics.Counter
}

var (
	globalVectorMetrics *vectorMetrics
	vectorMetricsOnce   sync.Once
)

// newVectorMetrics creates and registers vector storage metrics.
// Uses sync.Once to ensure metrics are only registered once globally.
func newVectorMetrics() *vectorMetrics {
	vectorMetricsOnce.Do(func() {
		globalVectorMetrics = createVectorMetrics()
	})
	return globalVectorMetrics
}

// createVectorMetrics creates the actual metrics instances.
func createVectorMetrics() *vectorMetrics {
	return &vectorMetrics{
		upsertOps: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "upsert_operations_total",
			Help:      "Total number of vector upsert operations",
		}),
		getOps: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "get_operations_total",
			Help:      "Total number of vector get operations",
		}),
		deleteOps: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "delete_operations_total",
			Help:      "Total number of vector delete operations",
		}),
		scanOps: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "scan_operations_total",
			Help:      "Total number of vector scan operations",
		}),
		batchCommits: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "batch_commits_total",
			Help:      "Total number of batch commits",
		}),
		namespaceOps: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "namespace_operations_total",
			Help:      "Total number of namespace operations",
		}),
		idAllocations: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "id_allocations_total",
			Help:      "Total number of ID allocations",
		}),
		upsertLatency: metrics.NewHistogram(metrics.HistogramOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "upsert_duration_seconds",
			Help:      "Histogram of vector upsert latencies",
			Buckets:   []float64{.0001, .0005, .001, .005, .01, .05, .1, .5, 1},
		}),
		getLatency: metrics.NewHistogram(metrics.HistogramOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "get_duration_seconds",
			Help:      "Histogram of vector get latencies",
			Buckets:   []float64{.0001, .0005, .001, .005, .01, .05, .1, .5, 1},
		}),
		deleteLatency: metrics.NewHistogram(metrics.HistogramOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "delete_duration_seconds",
			Help:      "Histogram of vector delete latencies",
			Buckets:   []float64{.0001, .0005, .001, .005, .01, .05, .1, .5, 1},
		}),
		scanLatency: metrics.NewHistogram(metrics.HistogramOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "scan_duration_seconds",
			Help:      "Histogram of vector scan latencies",
			Buckets:   []float64{.001, .005, .01, .05, .1, .5, 1, 5, 10},
		}),
		batchLatency: metrics.NewHistogram(metrics.HistogramOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "batch_duration_seconds",
			Help:      "Histogram of batch operation latencies",
			Buckets:   []float64{.001, .005, .01, .05, .1, .5, 1, 5, 10},
		}),
		bytesRead: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "bytes_read_total",
			Help:      "Total number of bytes read",
		}),
		bytesWritten: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "bytes_written_total",
			Help:      "Total number of bytes written",
		}),
		vectorsStored: metrics.NewGauge(metrics.GaugeOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "vectors_stored",
			Help:      "Current number of vectors stored",
		}),
		namespacesCount: metrics.NewGauge(metrics.GaugeOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "namespaces_count",
			Help:      "Current number of namespaces",
		}),
		errors: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector_storage",
			Name:      "errors_total",
			Help:      "Total number of vector storage errors",
		}),
	}
}

// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package syncwatchtest holds the test doubles for code that times its syncs
// through a syncwatch.Observer: a slog handler that captures the slow-sync
// warnings, and a reader for one histogram series in a Prometheus gatherer.
package syncwatchtest

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// FreshRegistry points the process metrics registry (pkg/util/metrics) at a
// new registry carrying the Go runtime and process collectors, as the
// default one does, and returns it. Cleanup swaps in another new registry
// rather than restoring the previous one: SetRegistry drops the metric
// cache, so a restored registry would be asked to register again a series
// it already holds, and panic.
func FreshRegistry(t testing.TB) *prometheus.Registry {
	t.Helper()
	reg := newProcessRegistry()
	metrics.SetRegistry(reg)
	t.Cleanup(func() { metrics.SetRegistry(newProcessRegistry()) })
	return reg
}

func newProcessRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return reg
}

// Record is one captured log record, its attributes flattened to a map
// keyed by attribute name and holding each attribute's resolved value.
type Record struct {
	Level   slog.Level
	Message string
	Attrs   map[string]any
}

// Log captures every record logged through Logger. It is safe for
// concurrent use.
type Log struct {
	mu      sync.Mutex
	records []Record
}

// NewLog returns an empty Log.
func NewLog() *Log { return &Log{} }

// Logger returns a logger whose records, at every level, land in l.
func (l *Log) Logger() *slog.Logger { return slog.New(&handler{log: l}) }

// Records returns a copy of the records captured so far.
func (l *Log) Records() []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Record(nil), l.records...)
}

// handler is the slog.Handler behind Log.Logger. attrs holds what WithAttrs
// bound; groups are flattened, which is all a warning-shape assertion needs.
type handler struct {
	log   *Log
	attrs []slog.Attr
}

func (h *handler) Enabled(context.Context, slog.Level) bool { return true }

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	rec := Record{Level: r.Level, Message: r.Message, Attrs: make(map[string]any, len(h.attrs)+r.NumAttrs())}
	for _, a := range h.attrs {
		rec.Attrs[a.Key] = a.Value.Resolve().Any()
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs[a.Key] = a.Value.Resolve().Any()
		return true
	})
	h.log.mu.Lock()
	h.log.records = append(h.log.records, rec)
	h.log.mu.Unlock()
	return nil
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &handler{log: h.log, attrs: append(append([]slog.Attr(nil), h.attrs...), attrs...)}
}

func (h *handler) WithGroup(string) slog.Handler { return h }

// HistogramSample returns the sample count and sum of the histogram series
// name{label=value} in g, failing t when g holds no such series. An empty
// label selects the family's first series, for an unlabelled histogram.
func HistogramSample(t testing.TB, g prometheus.Gatherer, name, label, value string) (count uint64, sum float64) {
	t.Helper()
	families, err := g.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if label == "" {
				h := m.GetHistogram()
				return h.GetSampleCount(), h.GetSampleSum()
			}
			for _, lp := range m.GetLabel() {
				if lp.GetName() == label && lp.GetValue() == value {
					h := m.GetHistogram()
					return h.GetSampleCount(), h.GetSampleSum()
				}
			}
		}
	}
	t.Fatalf("no histogram series %s{%s=%q}", name, label, value)
	return 0, 0
}

// HasFamily reports whether g exposes a metric family called name.
func HasFamily(t testing.TB, g prometheus.Gatherer, name string) bool {
	t.Helper()
	families, err := g.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() == name {
			return true
		}
	}
	return false
}

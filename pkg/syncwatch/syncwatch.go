// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package syncwatch times the durable syncs a node makes and warns when one is
// slow.
//
// Raft's durability contract makes every acknowledged write wait for an
// fsync, so a disk whose syncs stall for seconds costs the node its
// leadership. An Observer records each sync's duration and logs one warning
// per sync that exceeds its threshold, naming the threshold, the measured
// duration and what was syncing, so the operator can tell why. The default
// threshold is etcd's "slow fdatasync" default, one second; a node's boot
// sets the process's threshold from observability.slow_sync_threshold with
// SetSlowThreshold, and every Observer built without WithThreshold applies
// it. The threshold only decides when a sync is reported: it never changes
// whether, or how, a sync happens.
//
// An Observer serves two kinds of caller. A storage backend times its own
// syncs: it reads Now before the sync and calls Since after it, and the
// duration lands in qrdb_storage_sync_duration_seconds{backend}. The Raft WAL
// is timed by go-quicraft, which records its own histogram and reports each
// duration to an Observer built with ForWAL, through Observe.
package syncwatch

import (
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// DefaultSlowThreshold is the sync duration above which an Observer logs a
// warning: etcd's slow-fdatasync default.
const DefaultSlowThreshold = time.Second

// slowThreshold is the process's threshold in nanoseconds; zero means
// DefaultSlowThreshold.
var slowThreshold atomic.Int64

// SetSlowThreshold sets the threshold every Observer built without
// WithThreshold applies, those already built included: the process-wide
// counterpart of the default logger the warnings go to. A non-positive d
// restores DefaultSlowThreshold.
func SetSlowThreshold(d time.Duration) {
	slowThreshold.Store(int64(max(d, 0)))
}

// SlowThreshold returns the threshold SetSlowThreshold set, or
// DefaultSlowThreshold.
func SlowThreshold() time.Duration {
	if d := time.Duration(slowThreshold.Load()); d > 0 {
		return d
	}
	return DefaultSlowThreshold
}

// Backend names, the values of qrdb_storage_sync_duration_seconds' backend
// label.
const (
	BackendPebble = "pebble"
	BackendFile   = "file"
)

// Warning messages, one per kind of sync.
const (
	msgSlowStorageSync = "slow storage sync"
	msgSlowWALFsync    = "slow raft WAL fsync"
)

// syncBuckets spans 1 ms to 8.192 s in doubling steps: a healthy fsync lands
// in the first few buckets and the stalls that cost a Raft leader its
// heartbeat land in the last ones.
var syncBuckets = []float64{
	0.001, 0.002, 0.004, 0.008, 0.016, 0.032, 0.064,
	0.128, 0.256, 0.512, 1.024, 2.048, 4.096, 8.192,
}

// recorder receives each measured duration in seconds.
type recorder interface {
	Observe(seconds float64)
}

// Observer times syncs, records their durations and warns on slow ones. Its
// methods are safe for concurrent use and a nil *Observer is a no-op, so a
// backend built without one simply goes untimed.
type Observer struct {
	recorder recorder
	// threshold is WithThreshold's value; zero follows SlowThreshold.
	threshold time.Duration
	logger    *slog.Logger
	clock     clock.Clock
	msg       string
	attrs     []any
}

// Option configures an Observer.
type Option func(*Observer)

// WithClock sets the clock Now and Since read. The default is clock.Real;
// tests inject a clocktest.Fake to drive durations without sleeping.
func WithClock(c clock.Clock) Option {
	return func(o *Observer) {
		if c != nil {
			o.clock = c
		}
	}
}

// WithLogger sets the logger slow-sync warnings go to. The default is the
// slog default logger at the time of the warning.
func WithLogger(l *slog.Logger) Option {
	return func(o *Observer) { o.logger = l }
}

// WithThreshold pins the duration above which a sync is logged as slow,
// overriding the process threshold. A non-positive value is ignored.
func WithThreshold(d time.Duration) Option {
	return func(o *Observer) {
		if d > 0 {
			o.threshold = d
		}
	}
}

// WithAttrs adds what was syncing to every warning: the shard, the database,
// the directory. Each call appends to the attributes already set.
func WithAttrs(attrs ...slog.Attr) Option {
	return func(o *Observer) {
		for _, a := range attrs {
			o.attrs = append(o.attrs, a)
		}
	}
}

// ForStorage returns an Observer for a storage backend's syncs. Durations are
// recorded in qrdb_storage_sync_duration_seconds{backend} and every warning
// names the backend.
func ForStorage(backend string, opts ...Option) *Observer {
	o := newObserver(msgSlowStorageSync, storageSyncHistogram().With(backend), opts)
	o.attrs = append([]any{slog.String("backend", backend)}, o.attrs...)
	return o
}

// registeredHistogram is a histogram vector and the process registry it is
// registered in.
type registeredHistogram struct {
	// reg is the process registry vec was registered with, held as an
	// identity only, so this package does not import Prometheus itself.
	reg any
	vec *metrics.HistogramVec
}

// storageHistogram caches qrdb_storage_sync_duration_seconds. ForStorage
// runs for every storage wrapper a state machine builds over its database,
// some of them once per write, so the vector is looked up in the metrics
// package's registration cache only when the process registry has changed
// (a test swapping it) since the last lookup.
var storageHistogram atomic.Pointer[registeredHistogram]

func storageSyncHistogram() *metrics.HistogramVec {
	reg := metrics.Registry()
	if h := storageHistogram.Load(); h != nil && h.reg == reg {
		return h.vec
	}
	vec := metrics.NewHistogramVec(metrics.HistogramOpts{
		Namespace: "qrdb",
		Subsystem: "storage",
		Name:      "sync_duration_seconds",
		Help:      "Duration of a durable storage sync (a commit with sync, or an fsync), by backend.",
		Buckets:   syncBuckets,
	}, []string{"backend"})
	storageHistogram.Store(&registeredHistogram{reg: reg, vec: vec})
	return vec
}

// ForWAL returns an Observer for the Raft WAL's fsyncs. It records nothing:
// go-quicraft records the duration in quicraft_logdb_fsync_latency_seconds
// before handing it to Observe, which only checks it against the threshold.
func ForWAL(opts ...Option) *Observer {
	return newObserver(msgSlowWALFsync, nil, opts)
}

func newObserver(msg string, rec recorder, opts []Option) *Observer {
	o := &Observer{
		recorder: rec,
		clock:    clock.Real(),
		msg:      msg,
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// Threshold returns the duration above which a sync is logged as slow: the
// pinned one, or the process threshold.
func (o *Observer) Threshold() time.Duration {
	if o == nil || o.threshold <= 0 {
		return SlowThreshold()
	}
	return o.threshold
}

// Now reads the Observer's clock: the start of a sync Since then measures.
func (o *Observer) Now() time.Time {
	if o == nil {
		return time.Time{}
	}
	return o.clock.Now()
}

// Since observes the duration of a sync that started at start, a value read
// from Now.
func (o *Observer) Since(start time.Time) {
	if o == nil {
		return
	}
	o.Observe(o.clock.Since(start))
}

// Observe records one sync's duration and logs a warning when it exceeds the
// threshold.
func (o *Observer) Observe(d time.Duration) {
	if o == nil {
		return
	}
	if o.recorder != nil {
		o.recorder.Observe(d.Seconds())
	}
	threshold := o.Threshold()
	if d <= threshold {
		return
	}
	logger := o.logger
	if logger == nil {
		logger = slog.Default()
	}
	args := make([]any, 0, len(o.attrs)+2)
	args = append(args, slog.Duration("threshold", threshold), slog.Duration("took", d))
	args = append(args, o.attrs...)
	logger.Warn(o.msg, args...)
}

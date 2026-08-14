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

// Package batch re-exports the batch aggregator from go-quicraft.
//
// Applications should import this package rather than go-quicraft directly.
// The aggregator batches multiple write operations and flushes them
// periodically or when a size threshold is reached, amortizing the
// per-proposal Raft consensus overhead.
package batch

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	qcbatch "github.com/jeremyhahn/go-quicraft/pkg/batch"
)

// Type aliases for the core batch types.
type (
	// Proposer defines the interface for submitting proposals to Raft.
	Proposer = qcbatch.Proposer

	// Config configures the batch aggregator.
	Config = qcbatch.Config

	// Option configures the aggregator via the functional options pattern.
	Option = qcbatch.Option

	// Aggregator batches multiple write operations and flushes them
	// periodically or when a size threshold is reached.
	Aggregator = qcbatch.Aggregator
)

// Default configuration constants.
const (
	// DefaultMaxBatchSize is the maximum number of proposals collected
	// before a flush is triggered.
	DefaultMaxBatchSize = qcbatch.DefaultMaxBatchSize

	// DefaultFlushInterval is the maximum time between batch flushes.
	DefaultFlushInterval = qcbatch.DefaultFlushInterval

	// DefaultQueueSize is the capacity of the proposal channel.
	DefaultQueueSize = qcbatch.DefaultQueueSize
)

// DefaultConfig returns a Config with production-ready default values.
func DefaultConfig() Config {
	return qcbatch.DefaultConfig()
}

// WithConfig sets custom batch configuration.
func WithConfig(cfg Config) Option {
	return qcbatch.WithConfig(cfg)
}

// WithLogger sets a custom structured logger.
func WithLogger(logger *slog.Logger) Option {
	return qcbatch.WithLogger(logger)
}

// WithRegistry sets a custom Prometheus registerer for metrics.
func WithRegistry(reg prometheus.Registerer) Option {
	return qcbatch.WithRegistry(reg)
}

// NewAggregator creates a batch aggregator that groups proposals and
// flushes them through the given Proposer.
func NewAggregator(proposer Proposer, opts ...Option) (*Aggregator, error) {
	return qcbatch.NewAggregator(proposer, opts...)
}

// EncodeBatch combines multiple command payloads into a single batch.
// Format: [cmd1_len:4 BE][cmd1_data][cmd2_len:4 BE][cmd2_data]...
//
// It returns an error when a command cannot be represented in the format: the
// length prefix is a uint32, so a command at or above MaxInt32 would encode to
// a batch that DecodeBatch is required to reject. Refusing at encode time is
// what keeps the two ends symmetric — the alternative is a batch that commits
// durably and is then permanently undecodable.
func EncodeBatch(cmds [][]byte) ([]byte, error) {
	return qcbatch.EncodeBatch(cmds)
}

// DecodeBatch splits a batch payload into individual commands.
func DecodeBatch(data []byte) ([][]byte, error) {
	return qcbatch.DecodeBatch(data)
}

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

package batch

import (
	qcbatch "github.com/jeremyhahn/go-quicraft/pkg/batch"
)

// Sentinel errors re-exported from go-quicraft.
var (
	// ErrClosed is returned when Submit is called on a closed aggregator.
	ErrClosed = qcbatch.ErrClosed

	// ErrQueueFull is returned when the proposal queue has reached capacity.
	ErrQueueFull = qcbatch.ErrQueueFull

	// ErrNilProposer is returned when NewAggregator is called with nil Proposer.
	ErrNilProposer = qcbatch.ErrNilProposer

	// ErrFlush is returned when a flush operation fails.
	ErrFlush = qcbatch.ErrFlush

	// ErrDecode is returned when batch decoding encounters malformed data.
	ErrDecode = qcbatch.ErrDecode
)

// Error type aliases re-exported from go-quicraft.
type (
	// ClosedError is returned when an operation is attempted on a closed aggregator.
	ClosedError = qcbatch.ClosedError

	// QueueFullError is returned when the proposal queue has reached capacity.
	QueueFullError = qcbatch.QueueFullError

	// NilProposerError is returned when a nil Proposer is passed to NewAggregator.
	NilProposerError = qcbatch.NilProposerError

	// FlushError wraps a flush failure with shard context for diagnostics.
	FlushError = qcbatch.FlushError

	// DecodeError is returned when batch decoding encounters malformed data.
	DecodeError = qcbatch.DecodeError
)

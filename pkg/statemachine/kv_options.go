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

package statemachine

import (
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
)

// KVOption configures a KVStateMachine at construction time using the
// functional options pattern.
type KVOption func(*KVStateMachine)

// WithKVWatchManager attaches a WatchManager that receives key-change
// notifications for every successful write or delete.
func WithKVWatchManager(wm WatchManager) KVOption {
	return func(k *KVStateMachine) {
		k.watchManager = wm
	}
}

// WithKVLogger sets the structured logger for the state machine.
func WithKVLogger(logger *slog.Logger) KVOption {
	return func(k *KVStateMachine) {
		k.logger = logger
	}
}

// WithKVIndexManager attaches an IndexManager that handles secondary
// index create, drop, query, scan, and list operations.
func WithKVIndexManager(im IndexManager) KVOption {
	return func(k *KVStateMachine) {
		k.indexManager = im
	}
}

// WithKVLessor makes lr the lessor the state machine keeps its leases in.
// The composition root passes the lessor its leader-side expiry sweeper
// (lease.Lessor.RunExpiry) watches, so an expired lease is revoked and its
// keys deleted. Without it the state machine has a lessor of its own.
//
// A Lessor serves exactly one replica: the state machine restores it from its
// own lease table, which would erase a second replica's leases. A nil lr is
// ignored.
func WithKVLessor(lr *lease.Lessor) KVOption {
	return func(k *KVStateMachine) {
		if lr != nil {
			k.lessor = lr
		}
	}
}

// WithKVTimeSeriesStore attaches a TimeSeriesStore that handles
// time-series write, delete, retention, and query operations. When not
// set, time-series operations return ServiceNotConfiguredError.
func WithKVTimeSeriesStore(ts TimeSeriesStore) KVOption {
	return func(k *KVStateMachine) {
		k.tsStore = ts
	}
}

// WithKVMVCCStore attaches an MVCCStore that handles MVCC put, delete,
// range, compact, and transaction operations. When not set, MVCC
// operations return ServiceNotConfiguredError.
func WithKVMVCCStore(ms MVCCStore) KVOption {
	return func(k *KVStateMachine) {
		k.mvccStore = ms
	}
}

// WithKVKeyLengthCapGate injects the decision of whether validateClientKey
// may enforce its Apply-time key-length refusal (go-qrdb#592 F5). Without
// it the refusal is enforced unconditionally; see KeyLengthCapGate.
func WithKVKeyLengthCapGate(gate KeyLengthCapGate) KVOption {
	return func(k *KVStateMachine) {
		k.keyLengthGate = gate
	}
}

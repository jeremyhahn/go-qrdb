// Copyright (c) 2026 Jeremy Hahn
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

package service

import (
	"context"
	"errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// storeWrapError stands for a store layer above this package that wraps a read
// error with Unwrap, as tenant.StoreError does.
type storeWrapError struct{ err error }

func (e *storeWrapError) Error() string { return "store: " + e.err.Error() }
func (e *storeWrapError) Unwrap() error { return e.err }

// TestLeadershipTransient_ElectionErrors: every error of a shard electing a
// leader or moving it is transient, bare or wrapped as a read failure and
// again by a store above it.
func TestLeadershipTransient_ElectionErrors(t *testing.T) {
	cases := map[string]error{
		"timeout":                 quicraft.ErrTimeout,
		"aborted":                 quicraft.ErrAborted,
		"quicraft not leader":     quicraft.ErrNotLeader,
		"shard not ready":         quicraft.ErrShardNotReady,
		"read index not ready":    quicraft.ErrReadIndexNotReady,
		"transfer in progress":    quicraft.ErrTransferInProgress,
		"not leader, none known":  &NotLeaderError{ShardID: 1},
		"not leader, leader read": &NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "db.example.com:7000"},
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if !LeadershipTransient(err) {
				t.Errorf("LeadershipTransient(%v) = false, want true", err)
			}
			wrapped := &storeWrapError{err: &ReadError{Err: err}}
			if !LeadershipTransient(wrapped) {
				t.Errorf("LeadershipTransient(%v) = false, want true through wrappers", wrapped)
			}
		})
	}
}

// TestLeadershipTransient_OtherErrorsAreFinal: nothing that is not an
// election is transient, so a caller waiting one out never masks it.
func TestLeadershipTransient_OtherErrorsAreFinal(t *testing.T) {
	cases := map[string]error{
		"nil":              nil,
		"key not found":    &ReadError{Err: &KeyNotFoundError{Key: "k"}},
		"canceled":         quicraft.ErrCanceled,
		"busy":             quicraft.ErrBusy,
		"dropped":          quicraft.ErrDropped,
		"rejected":         quicraft.ErrRejected,
		"context canceled": context.Canceled,
		"plain":            errors.New("tenant record corrupt"),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if LeadershipTransient(err) {
				t.Errorf("LeadershipTransient(%v) = true, want false", err)
			}
			if err != nil && LeadershipTransient(&storeWrapError{err: err}) {
				t.Errorf("LeadershipTransient(wrapped %v) = true, want false", err)
			}
		})
	}
}

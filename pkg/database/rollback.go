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

package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// DropRollbackTimeout bounds how long the rollback of a failed drop may take
// to clear the fences the drop set.
const DropRollbackTimeout = 30 * time.Second

// DropRollbackContext returns the context a failed drop's rollback runs
// under: ctx's values without its cancellation, bounded by
// DropRollbackTimeout.
//
// A drop most often fails BECAUSE its context was cancelled or timed out --
// the caller gave up, or a proposal did not commit in time. Clearing the
// fences under that same context would fail for the same reason and leave
// the database refusing every write while it looks live. The rollback is
// part of the failure path, not of the caller's request, so it outlives the
// request -- but not indefinitely.
func DropRollbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), DropRollbackTimeout)
}

// NewIncarnation returns a fresh database incarnation: 128 random bits,
// hex-encoded. Hex is load-bearing -- the dropped tombstone a fence marker
// holds starts with a NUL byte, so no incarnation can ever equal it.
func NewIncarnation() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", &IncarnationError{Err: err}
	}
	return hex.EncodeToString(b[:]), nil
}

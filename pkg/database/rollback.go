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

type keepFenceKey struct{}

// KeepFenceOnFailure marks ctx so a drop run under it never lifts its fence
// when it fails: the in-memory Dropping flag and the durable drop claim both
// stay, and the database stays unservable until a retried drop lands. It is
// for a rollback that must not leave the database servable even for a
// moment -- a create refused by the strict-isolation tightening handshake,
// whose data would otherwise be served from a node-keyed log the tenant's
// strict policy forbids.
func KeepFenceOnFailure(ctx context.Context) context.Context {
	return context.WithValue(ctx, keepFenceKey{}, true)
}

func keepFenceOnFailure(ctx context.Context) bool {
	keep, _ := ctx.Value(keepFenceKey{}).(bool)
	return keep
}

type createGuardKey struct{}

// CreateGuard is a check a create runs once its catalog record is written
// and before anything serves the database: before its backend is opened,
// its keyspace unfenced or the database published. A non-nil error refuses
// the create, which then removes everything it made, so the database is
// never readable or writable.
type CreateGuard func(ctx context.Context) error

// WithCreateGuard marks ctx so a create run under it runs guard between its
// catalog write and its first serving step. It is for a check that must
// read state after the catalog record exists -- the create side of the
// strict-isolation tightening handshake, which a tightening that lists the
// catalog after the record lands is refused by instead -- yet refuse the
// database before any request can write to it.
func WithCreateGuard(ctx context.Context, guard CreateGuard) context.Context {
	return context.WithValue(ctx, createGuardKey{}, guard)
}

func createGuard(ctx context.Context) CreateGuard {
	guard, _ := ctx.Value(createGuardKey{}).(CreateGuard)
	return guard
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

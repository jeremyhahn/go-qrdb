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

package server

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// recordingVectorLocker stands in for a real service.VectorNamespaceLocker,
// recording the order in which locks are acquired and released so a test
// can assert that a write holds its lock across the check AND the write.
type recordingVectorLocker struct {
	mu     sync.Mutex
	events []string
}

func (l *recordingVectorLocker) record(event string) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *recordingVectorLocker) LockVectorNamespaceWrite(namespace string) func() {
	l.record("write-lock:" + namespace)
	return func() { l.record("write-unlock:" + namespace) }
}

func (l *recordingVectorLocker) LockVectorNamespaceReclaim(namespace string) func() {
	l.record("reclaim-lock:" + namespace)
	return func() { l.record("reclaim-unlock:" + namespace) }
}

// recordingVectorServicer records which mutating call ran and lets the test
// control its outcome. Only the methods a test exercises are overridden;
// every other call panics via the nil embedded VectorServicer.
type recordingVectorServicer struct {
	service.VectorServicer
	called    []string
	upsertErr error
}

func (s *recordingVectorServicer) UpsertVector(ctx context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	s.called = append(s.called, "UpsertVector")
	if s.upsertErr != nil {
		return nil, s.upsertErr
	}
	return &service.UpsertVectorResponse{ID: req.ID, Success: true}, nil
}

// lockingVectorServicer composes a VectorServicer with a VectorNamespaceLocker
// so newNamespacedVectorService's type assertion finds the locker.
type lockingVectorServicer struct {
	service.VectorServicer
	*recordingVectorLocker
}

func TestNamespacedVectorService_WriteLocksAcrossTheCheckAndTheWrite(t *testing.T) {
	locker := &recordingVectorLocker{}
	inner := &recordingVectorServicer{}
	checked := false
	view := &namespacedVectorService{
		dropFence: dropFence{check: func() error { checked = true; return nil }},
		inner:     inner,
		prefix:    "acme/orders/",
		locker:    locker,
	}

	resp, err := view.UpsertVector(context.Background(), &service.UpsertVectorRequest{ID: 1, Namespace: "default"})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.True(t, checked)
	assert.Equal(t, []string{"UpsertVector"}, inner.called)
	assert.Equal(t, []string{"write-lock:acme/orders/", "write-unlock:acme/orders/"}, locker.events)
}

func TestNamespacedVectorService_RefusalNeverReachesTheInnerWrite(t *testing.T) {
	locker := &recordingVectorLocker{}
	inner := &recordingVectorServicer{}
	refusal := errors.New("dropping")
	view := &namespacedVectorService{
		dropFence: dropFence{check: func() error { return refusal }},
		inner:     inner,
		prefix:    "acme/orders/",
		locker:    locker,
	}

	_, err := view.UpsertVector(context.Background(), &service.UpsertVectorRequest{ID: 1, Namespace: "default"})
	require.ErrorIs(t, err, refusal)
	assert.Empty(t, inner.called)
	// The lock is still released even though the write never ran: a
	// refusal must not leak the namespace lock.
	assert.Equal(t, []string{"write-lock:acme/orders/", "write-unlock:acme/orders/"}, locker.events)
}

func TestNamespacedVectorService_UnlocksEvenWhenTheInnerWriteFails(t *testing.T) {
	locker := &recordingVectorLocker{}
	writeErr := errors.New("storage unavailable")
	inner := &recordingVectorServicer{upsertErr: writeErr}
	view := &namespacedVectorService{
		dropFence: dropFence{check: func() error { return nil }},
		inner:     inner,
		prefix:    "acme/orders/",
		locker:    locker,
	}

	_, err := view.UpsertVector(context.Background(), &service.UpsertVectorRequest{ID: 1, Namespace: "default"})
	require.ErrorIs(t, err, writeErr)
	assert.Equal(t, []string{"write-lock:acme/orders/", "write-unlock:acme/orders/"}, locker.events)
}

func TestNewNamespacedVectorService_DetectsALocker(t *testing.T) {
	locker := &recordingVectorLocker{}
	inner := &lockingVectorServicer{VectorServicer: &recordingVectorServicer{}, recordingVectorLocker: locker}

	view := newNamespacedVectorService(inner, "acme/orders/")
	assert.NotNil(t, view.locker)
}

func TestNewNamespacedVectorService_NoLockerIsFine(t *testing.T) {
	view := newNamespacedVectorService(&recordingVectorServicer{}, "acme/orders/")
	assert.Nil(t, view.locker)

	// A write still works -- guardedWrite with no locker just skips locking.
	resp, err := view.UpsertVector(context.Background(), &service.UpsertVectorRequest{ID: 1, Namespace: "default"})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

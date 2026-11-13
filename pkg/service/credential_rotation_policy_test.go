// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

func TestCredentialService_SetRotationPolicy_RoundTrips(t *testing.T) {
	svc := newTestBarrierCredentialServiceSvc(t)
	ctx := context.Background()
	require.NoError(t, svc.CredentialStore(ctx, "db", []byte("v1")))

	policy := credentials.RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}
	require.NoError(t, svc.CredentialSetRotationPolicy(ctx, "db", policy))

	status, err := svc.CredentialRotationPolicy(ctx, "db")
	require.NoError(t, err)
	assert.Equal(t, policy, status.Policy)
	assert.False(t, status.NextRotation.IsZero(), "an enabled policy is scheduled")
}

// metaReadHookStore runs hook after every read of a credential metadata
// record, outside any read the hook itself makes: a SetRotationPolicy that
// lands between any two metadata reads of one CredentialRotationPolicy.
type metaReadHookStore struct {
	credentials.Store
	hook    func()
	running bool
}

func (s *metaReadHookStore) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := s.Store.Get(ctx, key)
	if s.hook != nil && !s.running && strings.HasPrefix(key, credentials.MetaKeyPrefix) {
		s.running = true
		s.hook()
		s.running = false
	}
	return v, err
}

// TestCredentialService_RotationPolicy_OneRecordUnderAConcurrentUpdate pins
// that CredentialRotationPolicy reports the policy and its schedule from
// one metadata record: a SetRotationPolicy that toggles the policy after
// every metadata read never yields an enabled policy with no next rotation,
// or a disabled one with a next rotation.
func TestCredentialService_RotationPolicy_OneRecordUnderAConcurrentUpdate(t *testing.T) {
	ctx := context.Background()
	barrier, _ := newTestSoftwareBarrier(t)
	require.NoError(t, barrier.Initialize(ctx, seal.MustCredentials(ctx, []byte("my-secret"))))
	store := &metaReadHookStore{Store: barrier}
	inner, err := credentials.NewCredentialService(
		&credentials.Config{Strategy: credentials.StrategyBarrier}, store, testLogger())
	require.NoError(t, err)
	svc := NewCredentialService(inner)

	require.NoError(t, svc.CredentialStore(ctx, "db", []byte("v1")))
	enabled := credentials.RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}
	disabled := credentials.RotationPolicy{Enabled: false, Interval: time.Hour, RetainVersions: 2}
	require.NoError(t, svc.CredentialSetRotationPolicy(ctx, "db", enabled))

	on := true
	store.hook = func() {
		next := disabled
		if !on {
			next = enabled
		}
		require.NoError(t, inner.SetRotationPolicy(ctx, "db", next))
		on = !on
	}
	for range 4 {
		status, err := svc.CredentialRotationPolicy(ctx, "db")
		require.NoError(t, err)
		assert.Equal(t, status.Policy.Enabled, !status.NextRotation.IsZero(),
			"policy %+v with next rotation %v is a status no record held", status.Policy, status.NextRotation)
	}
}

func TestCredentialService_SetRotationPolicy_InvalidRefused(t *testing.T) {
	svc := newTestBarrierCredentialServiceSvc(t)
	ctx := context.Background()
	require.NoError(t, svc.CredentialStore(ctx, "db", []byte("v1")))

	err := svc.CredentialSetRotationPolicy(ctx, "db", credentials.RotationPolicy{Enabled: true})
	var invalid *credentials.InvalidRotationPolicyError
	assert.True(t, errors.As(err, &invalid), "err = %v", err)
}

func TestCredentialService_RotationPolicy_UnknownNameNotFound(t *testing.T) {
	svc := newTestBarrierCredentialServiceSvc(t)
	_, err := svc.CredentialRotationPolicy(context.Background(), "missing")
	assert.ErrorIs(t, err, credentials.ErrCredentialNotFound)
}

func TestCredentialService_RotationPolicy_UnsetIsZero(t *testing.T) {
	svc := newTestBarrierCredentialServiceSvc(t)
	ctx := context.Background()
	require.NoError(t, svc.CredentialStore(ctx, "db", []byte("v1")))

	status, err := svc.CredentialRotationPolicy(ctx, "db")
	require.NoError(t, err)
	assert.Equal(t, credentials.RotationPolicy{}, status.Policy)
	assert.True(t, status.NextRotation.IsZero())
}

func TestCredentialService_CredentialRotateDue_NoSourceReportsDue(t *testing.T) {
	svc := newTestBarrierCredentialServiceSvc(t).(*CredentialService)
	ctx := context.Background()
	require.NoError(t, svc.CredentialStore(ctx, "db", []byte("v1")))
	require.NoError(t, svc.CredentialSetRotationPolicy(ctx, "db",
		credentials.RotationPolicy{Enabled: true, Interval: time.Minute, RetainVersions: 1}))

	n, err := svc.CredentialRotateDue(ctx, time.Now().Add(time.Hour))
	assert.Zero(t, n)
	assert.ErrorIs(t, err, credentials.ErrNoRotationValueSource)
}

func TestCredentialService_CredentialRotateDue_NothingDue(t *testing.T) {
	svc := newTestBarrierCredentialServiceSvc(t).(*CredentialService)
	ctx := context.Background()
	require.NoError(t, svc.CredentialStore(ctx, "db", []byte("v1")))

	n, err := svc.CredentialRotateDue(ctx, time.Now())
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestCredentialRotationPolicyFromWire_Parses(t *testing.T) {
	got, err := CredentialRotationPolicyFromWire(true, "720h", 2)
	require.NoError(t, err)
	assert.Equal(t, credentials.RotationPolicy{Enabled: true, Interval: 720 * time.Hour, RetainVersions: 2}, got)

	got, err = CredentialRotationPolicyFromWire(false, "", 0)
	require.NoError(t, err)
	assert.Equal(t, credentials.RotationPolicy{}, got)
}

func TestCredentialRotationPolicyFromWire_BadIntervalRefused(t *testing.T) {
	_, err := CredentialRotationPolicyFromWire(true, "monthly", 1)
	var invalid *credentials.InvalidRotationPolicyError
	require.True(t, errors.As(err, &invalid), "err = %v", err)
	assert.Contains(t, invalid.Error(), "monthly")
}

func TestCredentialRotationPolicyStatus_WireForm(t *testing.T) {
	next := time.Date(2026, 11, 1, 12, 0, 0, 0, time.FixedZone("x", 3600))
	s := &CredentialRotationPolicyStatus{
		Policy:       credentials.RotationPolicy{Enabled: true, Interval: 90 * time.Minute, RetainVersions: 1},
		NextRotation: next,
	}
	assert.Equal(t, "1h30m0s", s.WireInterval())
	assert.Equal(t, "2026-11-01T11:00:00Z", s.WireNextRotation())

	unset := &CredentialRotationPolicyStatus{}
	assert.Equal(t, "0s", unset.WireInterval())
	assert.Empty(t, unset.WireNextRotation())
}

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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
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

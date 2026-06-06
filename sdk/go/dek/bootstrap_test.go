// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package dek_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	sdkdek "github.com/jeremyhahn/go-qrdb/sdk/go/dek"
)

// stubbedTenantServicer satisfies the TenantServicer interface for testing.
type stubbedTenantServicer struct {
	err error
}

func (s *stubbedTenantServicer) CreateTenant(ctx context.Context, id, name string, iso tenant.IsolationType) (*tenant.TenantInfo, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &tenant.TenantInfo{ID: id, Name: name, IsolationType: iso}, nil
}

func TestBootstrapSystemTenant_Created(t *testing.T) {
	svc := &stubbedTenantServicer{}
	result := sdkdek.BootstrapSystemTenant(context.Background(), svc)
	if result.State != sdkdek.BootstrapCreated {
		t.Errorf("expected BootstrapCreated, got %d", result.State)
	}
	if result.Err != nil {
		t.Errorf("expected nil Err, got %v", result.Err)
	}
}

func TestBootstrapSystemTenant_AlreadyExists(t *testing.T) {
	svc := &stubbedTenantServicer{err: tenant.ErrTenantAlreadyExists}
	result := sdkdek.BootstrapSystemTenant(context.Background(), svc)
	if result.State != sdkdek.BootstrapAlreadyExists {
		t.Errorf("expected BootstrapAlreadyExists, got %d", result.State)
	}
	if result.Err != nil {
		t.Errorf("expected nil Err, got %v", result.Err)
	}
}

func TestBootstrapSystemTenant_Deferred_StringMatch(t *testing.T) {
	sealErr := errors.New("barrier is sealed")
	svc := &stubbedTenantServicer{err: sealErr}
	result := sdkdek.BootstrapSystemTenant(context.Background(), svc)
	if result.State != sdkdek.BootstrapDeferred {
		t.Errorf("expected BootstrapDeferred, got %d", result.State)
	}
	if result.Err != nil {
		t.Errorf("expected nil Err on deferred, got %v", result.Err)
	}
}

func TestBootstrapSystemTenant_NonFatal(t *testing.T) {
	unexpectedErr := errors.New("some transient storage error")
	svc := &stubbedTenantServicer{err: unexpectedErr}
	result := sdkdek.BootstrapSystemTenant(context.Background(), svc)
	if result.State != sdkdek.BootstrapNonFatal {
		t.Errorf("expected BootstrapNonFatal, got %d", result.State)
	}
	if !errors.Is(result.Err, unexpectedErr) {
		t.Errorf("expected result.Err to wrap unexpectedErr, got %v", result.Err)
	}
}

func TestBootstrapSystemTenant_WithTimeout(t *testing.T) {
	// Ensure the option is accepted without panic.
	svc := &stubbedTenantServicer{}
	result := sdkdek.BootstrapSystemTenant(
		context.Background(),
		svc,
		sdkdek.WithBootstrapTimeout(2*time.Second),
	)
	if result.State != sdkdek.BootstrapCreated {
		t.Errorf("expected BootstrapCreated with custom timeout, got %d", result.State)
	}
}

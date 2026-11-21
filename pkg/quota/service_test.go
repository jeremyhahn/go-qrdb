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

package quota

import (
	"context"
	"errors"
	"testing"
)

// --- mock helpers ---

// staticProvider is a QuotaProvider that always returns the same Quotas.
type staticProvider struct {
	quotas *Quotas
	err    error
}

func (p *staticProvider) Quotas(_ context.Context, _ string) (*Quotas, error) {
	return p.quotas, p.err
}

// fixedCounter returns a CountFunc that always reports a fixed value.
func fixedCounter(n int64) CountFunc {
	return func(_ context.Context, _ string) (int64, error) {
		return n, nil
	}
}

// errorCounter returns a CountFunc that always returns the given error.
func errorCounter(err error) CountFunc {
	return func(_ context.Context, _ string) (int64, error) {
		return 0, err
	}
}

// --- NewService ---

func TestNewServiceNilProviderReturnsError(t *testing.T) {
	_, err := NewService(nil, nil)
	if err == nil {
		t.Fatal("NewService(nil, nil) expected error, got nil")
	}
	if !errors.Is(err, ErrNilProvider) {
		t.Errorf("NewService(nil, nil) error = %v, want ErrNilProvider", err)
	}
}

func TestNewServiceNilLoggerUsesDefault(t *testing.T) {
	p := &staticProvider{quotas: &Quotas{}}
	svc, err := NewService(p, nil)
	if err != nil {
		t.Fatalf("NewService(p, nil) returned unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("NewService(p, nil) returned nil service")
	}
	if svc.logger == nil {
		t.Error("NewService(p, nil) should set a non-nil logger via slog.Default()")
	}
}

// --- Check: unlimited (-1) ---

func TestCheckUnlimitedQuotaReturnsNil(t *testing.T) {
	p := &staticProvider{quotas: &Quotas{MaxKeys: -1}}
	svc, _ := NewService(p, nil)
	// No counter registered; should short-circuit before calling it.
	err := svc.Check(context.Background(), "tenant1", ResourceKeys)
	if err != nil {
		t.Errorf("Check with unlimited quota (-1) returned unexpected error: %v", err)
	}
}

// --- Check: zero (system default) ---

func TestCheckSystemDefaultQuotaReturnsNil(t *testing.T) {
	p := &staticProvider{quotas: &Quotas{MaxKeys: 0}}
	svc, _ := NewService(p, nil)
	err := svc.Check(context.Background(), "tenant1", ResourceKeys)
	if err != nil {
		t.Errorf("Check with zero quota (system default) returned unexpected error: %v", err)
	}
}

// --- Check: within limit ---

func TestCheckWithinLimitReturnsNil(t *testing.T) {
	p := &staticProvider{quotas: &Quotas{MaxKeys: 100}}
	svc, _ := NewService(p, nil)
	svc.RegisterCounter(ResourceKeys, fixedCounter(50))
	err := svc.Check(context.Background(), "tenant1", ResourceKeys)
	if err != nil {
		t.Errorf("Check within limit returned unexpected error: %v", err)
	}
}

func TestCheckAtLimitReturnsExceededError(t *testing.T) {
	p := &staticProvider{quotas: &Quotas{MaxKeys: 10}}
	svc, _ := NewService(p, nil)
	svc.RegisterCounter(ResourceKeys, fixedCounter(10))
	err := svc.Check(context.Background(), "acme", ResourceKeys)
	if err == nil {
		t.Fatal("Check at limit expected error, got nil")
	}
	var exceeded *ExceededError
	if !errors.As(err, &exceeded) {
		t.Errorf("Check at limit error type = %T, want *ExceededError", err)
	}
	if exceeded.TenantID != "acme" {
		t.Errorf("ExceededError.TenantID = %q, want %q", exceeded.TenantID, "acme")
	}
	if exceeded.Resource != ResourceKeys {
		t.Errorf("ExceededError.Resource = %v, want %v", exceeded.Resource, ResourceKeys)
	}
	if exceeded.Current != 10 {
		t.Errorf("ExceededError.Current = %d, want 10", exceeded.Current)
	}
	if exceeded.Limit != 10 {
		t.Errorf("ExceededError.Limit = %d, want 10", exceeded.Limit)
	}
}

// --- CheckN ---

func TestCheckNWithinLimitReturnsNil(t *testing.T) {
	p := &staticProvider{quotas: &Quotas{MaxUsers: 50}}
	svc, _ := NewService(p, nil)
	svc.RegisterCounter(ResourceUsers, fixedCounter(40))
	err := svc.CheckN(context.Background(), "t1", ResourceUsers, 5)
	if err != nil {
		t.Errorf("CheckN within limit returned unexpected error: %v", err)
	}
}

func TestCheckNExceedingLimitReturnsExceededError(t *testing.T) {
	p := &staticProvider{quotas: &Quotas{MaxUsers: 50}}
	svc, _ := NewService(p, nil)
	svc.RegisterCounter(ResourceUsers, fixedCounter(48))
	// current(48) + n(5) = 53 > limit(50)
	err := svc.CheckN(context.Background(), "t2", ResourceUsers, 5)
	if err == nil {
		t.Fatal("CheckN exceeding limit expected error, got nil")
	}
	var exceeded *ExceededError
	if !errors.As(err, &exceeded) {
		t.Errorf("CheckN exceeded error type = %T, want *ExceededError", err)
	}
}

// --- Usage ---

func TestUsageReturnsCounterValue(t *testing.T) {
	p := &staticProvider{quotas: &Quotas{}}
	svc, _ := NewService(p, nil)
	svc.RegisterCounter(ResourceRoles, fixedCounter(7))
	got, err := svc.Usage(context.Background(), "tenant1", ResourceRoles)
	if err != nil {
		t.Fatalf("Usage returned unexpected error: %v", err)
	}
	if got != 7 {
		t.Errorf("Usage() = %d, want 7", got)
	}
}

func TestUsageWithUnregisteredCounterReturnsError(t *testing.T) {
	p := &staticProvider{quotas: &Quotas{}}
	svc, _ := NewService(p, nil)
	_, err := svc.Usage(context.Background(), "tenant1", ResourceCertificates)
	if err == nil {
		t.Fatal("Usage with unregistered counter expected error, got nil")
	}
	if _, ok := err.(*UnregisteredCounterError); !ok {
		t.Errorf("Usage unregistered error type = %T, want *UnregisteredCounterError", err)
	}
}

// --- Check with unregistered counter ---

func TestCheckWithUnregisteredCounterReturnsError(t *testing.T) {
	p := &staticProvider{quotas: &Quotas{MaxBackups: 5}}
	svc, _ := NewService(p, nil)
	// No counter registered for ResourceBackups.
	err := svc.Check(context.Background(), "tenant1", ResourceBackups)
	if err == nil {
		t.Fatal("Check with unregistered counter expected error, got nil")
	}
	if _, ok := err.(*UnregisteredCounterError); !ok {
		t.Errorf("Check unregistered error type = %T, want *UnregisteredCounterError", err)
	}
}

// --- Counter error propagation ---

func TestCheckPropagatesCounterError(t *testing.T) {
	injected := errors.New("counter: storage unavailable")
	p := &staticProvider{quotas: &Quotas{MaxKeys: 100}}
	svc, _ := NewService(p, nil)
	svc.RegisterCounter(ResourceKeys, errorCounter(injected))
	err := svc.Check(context.Background(), "t", ResourceKeys)
	if !errors.Is(err, injected) {
		t.Errorf("Check counter error = %v, want %v", err, injected)
	}
}

// --- Provider error propagation ---

func TestCheckPropagatesProviderError(t *testing.T) {
	providerErr := errors.New("provider: unavailable")
	p := &staticProvider{err: providerErr}
	svc, _ := NewService(p, nil)
	err := svc.Check(context.Background(), "t", ResourceKeys)
	if !errors.Is(err, providerErr) {
		t.Errorf("Check provider error = %v, want %v", err, providerErr)
	}
}

// --- RegisterCounter replaces previous ---

func TestRegisterCounterReplacesExisting(t *testing.T) {
	p := &staticProvider{quotas: &Quotas{MaxKeys: 100}}
	svc, _ := NewService(p, nil)
	svc.RegisterCounter(ResourceKeys, fixedCounter(10))
	svc.RegisterCounter(ResourceKeys, fixedCounter(99))
	got, err := svc.Usage(context.Background(), "t", ResourceKeys)
	if err != nil {
		t.Fatalf("Usage after replace returned unexpected error: %v", err)
	}
	if got != 99 {
		t.Errorf("Usage after RegisterCounter replace = %d, want 99", got)
	}
}

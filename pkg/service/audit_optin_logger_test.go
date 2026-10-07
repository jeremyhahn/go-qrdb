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

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// fixedAuditEvent returns a deterministic AuditEvent for tenantID, for
// tests that only care about routing, not content.
func fixedAuditEvent(tenantID string) audit.AuditEvent {
	return audit.AuditEvent{
		TenantID:  tenantID,
		Actor:     "operator",
		Resource:  "barrier",
		Action:    "unseal",
		Outcome:   audit.OutcomeSuccess,
		Timestamp: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
}

// spyAuditLogger records every Emit/Query call and answers configurable
// results, for asserting an outer wrapper called (or did not call) it.
type spyAuditLogger struct {
	emitCalls  int
	queryCalls int
	emitErr    error
	queryPage  *audit.Page
	queryErr   error
	closeCalls int
}

func (s *spyAuditLogger) Emit(context.Context, audit.AuditEvent) error {
	s.emitCalls++
	return s.emitErr
}

func (s *spyAuditLogger) Query(context.Context, audit.Filter) (*audit.Page, error) {
	s.queryCalls++
	return s.queryPage, s.queryErr
}

func (s *spyAuditLogger) Close() error {
	s.closeCalls++
	return nil
}

// fakeEntitlementLookup answers ReplicatedAuditEnabled from a fixed map.
type fakeEntitlementLookup struct {
	enabled map[string]bool
	err     error
}

func (f *fakeEntitlementLookup) ReplicatedAuditEnabled(_ context.Context, tenantID string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.enabled[tenantID], nil
}

func TestOptInAuditLogger_Emit_OffIsANoOp(t *testing.T) {
	inner := &spyAuditLogger{}
	logger := NewOptInAuditLogger(inner, &fakeEntitlementLookup{enabled: map[string]bool{}}, false)
	if err := logger.Emit(context.Background(), fixedAuditEvent("acme")); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if inner.emitCalls != 0 {
		t.Fatalf("inner.Emit called %d times, want 0 (entitlement off must propose nothing)", inner.emitCalls)
	}
}

func TestOptInAuditLogger_Emit_OnDelegatesToInner(t *testing.T) {
	inner := &spyAuditLogger{}
	logger := NewOptInAuditLogger(inner, &fakeEntitlementLookup{enabled: map[string]bool{"acme": true}}, false)
	if err := logger.Emit(context.Background(), fixedAuditEvent("acme")); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if inner.emitCalls != 1 {
		t.Fatalf("inner.Emit called %d times, want 1", inner.emitCalls)
	}
}

func TestOptInAuditLogger_Emit_PropagatesLookupFailureRatherThanTreatingItAsOff(t *testing.T) {
	inner := &spyAuditLogger{}
	lookupErr := errors.New("store unavailable")
	logger := NewOptInAuditLogger(inner, &fakeEntitlementLookup{err: lookupErr}, false)
	err := logger.Emit(context.Background(), fixedAuditEvent("acme"))
	if !errors.Is(err, lookupErr) {
		t.Fatalf("err = %v, want %v", err, lookupErr)
	}
	if inner.emitCalls != 0 {
		t.Fatalf("inner.Emit called %d times, want 0", inner.emitCalls)
	}
}

func TestOptInAuditLogger_Query_OffAnswersNotAnswerable(t *testing.T) {
	inner := &spyAuditLogger{}
	logger := NewOptInAuditLogger(inner, &fakeEntitlementLookup{enabled: map[string]bool{}}, false)
	page, err := logger.Query(context.Background(), audit.Filter{TenantID: "acme"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if page.Total != -1 {
		t.Fatalf("Total = %d, want -1 (not 0: there is no trail, not an empty one)", page.Total)
	}
	if inner.queryCalls != 0 {
		t.Fatalf("inner.Query called %d times, want 0", inner.queryCalls)
	}
}

func TestOptInAuditLogger_Query_OnDelegatesToInner(t *testing.T) {
	want := &audit.Page{Total: 3}
	inner := &spyAuditLogger{queryPage: want}
	logger := NewOptInAuditLogger(inner, &fakeEntitlementLookup{enabled: map[string]bool{"acme": true}}, false)
	got, err := logger.Query(context.Background(), audit.Filter{TenantID: "acme"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got != want {
		t.Fatal("Query did not return inner's page")
	}
}

// TestOptInAuditLogger_SystemTenant_UsesPlatformEnabledNotEntitlement
// asserts the system tenant's own trail is governed by the constructor's
// platformEnabled flag, never by the (always-false-for-system-tenant)
// entitlement lookup.
func TestOptInAuditLogger_SystemTenant_UsesPlatformEnabledNotEntitlement(t *testing.T) {
	inner := &spyAuditLogger{}
	entitlement := &fakeEntitlementLookup{enabled: map[string]bool{tenantid.System: true}} // would be wrong if consulted
	loggerOff := NewOptInAuditLogger(inner, entitlement, false)
	if err := loggerOff.Emit(context.Background(), fixedAuditEvent(tenantid.System)); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if inner.emitCalls != 0 {
		t.Fatalf("platformEnabled=false: inner.Emit called %d times, want 0", inner.emitCalls)
	}

	loggerOn := NewOptInAuditLogger(inner, &fakeEntitlementLookup{enabled: map[string]bool{}}, true)
	if err := loggerOn.Emit(context.Background(), fixedAuditEvent(tenantid.System)); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if inner.emitCalls != 1 {
		t.Fatalf("platformEnabled=true: inner.Emit called %d times, want 1", inner.emitCalls)
	}
}

// TestOptInAuditLogger_Close_DelegatesToInner asserts Close is not
// swallowed by the entitlement wrapper.
func TestOptInAuditLogger_Close_DelegatesToInner(t *testing.T) {
	inner := &spyAuditLogger{}
	logger := NewOptInAuditLogger(inner, &fakeEntitlementLookup{}, false)
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if inner.closeCalls != 1 {
		t.Fatalf("inner.Close called %d times, want 1", inner.closeCalls)
	}
}

// spyReplicatedLogger is a spyAuditLogger that also implements
// TenantChainVerifier, for testing OptInAuditLogger.VerifyTenantChain
// without a real Host.
type spyReplicatedLogger struct {
	spyAuditLogger
	verifyCalls  int
	verifyReport *audit.ChainContinuityReport
	verifyErr    error
}

func (s *spyReplicatedLogger) VerifyTenantChain(context.Context, string) (*audit.ChainContinuityReport, error) {
	s.verifyCalls++
	return s.verifyReport, s.verifyErr
}

// TestOptInAuditLogger_VerifyTenantChain_OffIsRefused asserts a tenant
// whose entitlement is off gets *ReplicatedAuditNotEntitledError, never a
// misleading "verified" report for a chain that was never written.
func TestOptInAuditLogger_VerifyTenantChain_OffIsRefused(t *testing.T) {
	inner := &spyReplicatedLogger{}
	logger := NewOptInAuditLogger(inner, &fakeEntitlementLookup{enabled: map[string]bool{}}, false)
	_, err := logger.VerifyTenantChain(context.Background(), "acme")
	var refused *ReplicatedAuditNotEntitledError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want *ReplicatedAuditNotEntitledError", err)
	}
	if inner.verifyCalls != 0 {
		t.Fatalf("inner.VerifyTenantChain called %d times, want 0", inner.verifyCalls)
	}
}

// TestOptInAuditLogger_VerifyTenantChain_OnDelegatesToInner asserts an
// entitled tenant's verification reaches inner.
func TestOptInAuditLogger_VerifyTenantChain_OnDelegatesToInner(t *testing.T) {
	want := &audit.ChainContinuityReport{Verified: true, Records: 2, Head: 2, Low: 1, Genesis: true}
	inner := &spyReplicatedLogger{verifyReport: want}
	logger := NewOptInAuditLogger(inner, &fakeEntitlementLookup{enabled: map[string]bool{"acme": true}}, false)
	got, err := logger.VerifyTenantChain(context.Background(), "acme")
	if err != nil {
		t.Fatalf("VerifyTenantChain: %v", err)
	}
	if got != want {
		t.Fatal("VerifyTenantChain did not return inner's report")
	}
	if inner.verifyCalls != 1 {
		t.Fatalf("inner.VerifyTenantChain called %d times, want 1", inner.verifyCalls)
	}
}

// TestOptInAuditLogger_VerifyTenantChain_InnerWithoutVerifierSupport
// asserts a non-verifying inner (an ordinary audit.AuditLogger spy) answers
// a typed error rather than a panic.
func TestOptInAuditLogger_VerifyTenantChain_InnerWithoutVerifierSupport(t *testing.T) {
	inner := &spyAuditLogger{}
	logger := NewOptInAuditLogger(inner, &fakeEntitlementLookup{enabled: map[string]bool{"acme": true}}, false)
	if _, err := logger.VerifyTenantChain(context.Background(), "acme"); err == nil {
		t.Fatal("expected an error when inner does not implement TenantChainVerifier")
	}
}

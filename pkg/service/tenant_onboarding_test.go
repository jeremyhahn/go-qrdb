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
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// test infrastructure
//
// TenantOnboardingService is a thin orchestrator over bootstrap.ProvisionTenant;
// these tests exercise it over the SAME production collaborators the rest of
// this package already builds test doubles for (newCertEnrollmentProvider,
// newCertEnrollmentUserStore in cert_enrollment_test.go) plus a fresh
// tenant.TenantServicer and role.RoleServicer, so what is proven is the real
// seam wiring (validation, isolation defaulting, audit gating/settling,
// result mapping) rather than a mock standing in for it.
// ---------------------------------------------------------------------------

// newOnboardingTenantService returns a real, no-seal tenant.TenantServicer
// over a fresh in-memory store. It is sufficient for prefix-isolated tenants,
// which every test here creates.
func newOnboardingTenantService(t *testing.T) tenant.TenantServicer {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("tenant.NewKVTenantService: %v", err)
	}
	return svc
}

// newOnboardingRoleStore returns a real role.RoleServicer over a fresh
// in-memory store.
func newOnboardingRoleStore(t *testing.T) role.RoleServicer {
	t.Helper()
	svc, err := role.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("role.NewStore: %v", err)
	}
	return svc
}

// newOnboardingDeps returns a fully configured TenantOnboardingDeps: every
// required collaborator present, no audit sink. Individual tests override
// the fields they need to exercise. The no-seal tenant service seals nothing,
// so a pending admin's CSR can be signed by the same CA as any other here.
func newOnboardingDeps(t *testing.T) TenantOnboardingDeps {
	t.Helper()
	certs := newCertEnrollmentProvider(t)
	return TenantOnboardingDeps{
		Tenants:            newOnboardingTenantService(t),
		Users:              newCertEnrollmentUserStore(t),
		Roles:              newOnboardingRoleStore(t),
		Certs:              certs,
		Anchors:            trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore()),
		TrustDomain:        enrollmentTrustDomain,
		Logger:             slog.New(slog.DiscardHandler),
		PendingAdminSigner: &bootstrap.KeyBSigner{Certs: certs},
	}
}

// onboardingFailingGetTenant wraps a real tenant.TenantServicer, delegating
// every method except GetTenant, which it faithfully fails -- modeling a
// tenant read that times out after bootstrap.ProvisionTenant has committed
// the tenant, as a linearizable read does on a loaded cluster.
type onboardingFailingGetTenant struct {
	tenant.TenantServicer
	err error
}

func (f *onboardingFailingGetTenant) GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error) {
	return nil, f.err
}

// errOnboardingReadBack is what committedOnlyTenants answers every GetTenant
// with.
var errOnboardingReadBack = errors.New("tenant read back after its create")

// committedOnlyTenants wraps a real tenant.TenantServicer and fails every
// GetTenant: a create answers with the record its CreateTenant committed and
// never reads the tenant back. With autoUnseal set, CreateTenant answers as
// the real service does for a system-managed barrier tenant, whose barrier
// is serving by the time the record is committed; the double does not
// register a barrier, so DeleteTenant has no record to remove.
type committedOnlyTenants struct {
	tenant.TenantServicer
	autoUnseal bool
}

func (c *committedOnlyTenants) CreateTenant(ctx context.Context, id, name string, isolationType tenant.IsolationType, opts ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	if !c.autoUnseal {
		return c.TenantServicer.CreateTenant(ctx, id, name, isolationType, opts...)
	}
	now := time.Now().UTC()
	return &tenant.TenantInfo{ID: id, Name: name, IsolationType: isolationType, BarrierAutoUnseal: true, CreatedAt: now, UpdatedAt: now}, nil
}

func (c *committedOnlyTenants) GetTenant(context.Context, string) (*tenant.TenantInfo, error) {
	return nil, errOnboardingReadBack
}

// stallingAdminUsers is a user store whose CreateUser stalls until released,
// as a raft proposal does while its shard's leader settles: the slow proposer
// an auto-unseal tenant's admin account waits on. It closes entered when the
// first CreateUser starts.
type stallingAdminUsers struct {
	user.UserServicer
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *stallingAdminUsers) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.UserServicer.CreateUser(ctx, req)
}

// signallingPendingAdmins closes deleted once a pending admin record has
// been deleted, which is the last step of completing the admin's account.
type signallingPendingAdmins struct {
	tenantadmin.Store
	deleted chan struct{}
	once    sync.Once
}

func (s *signallingPendingAdmins) Delete(ctx context.Context, tenantID string, read *tenantadmin.Pending) (bool, error) {
	deleted, err := s.Store.Delete(ctx, tenantID, read)
	if err != nil {
		return false, err
	}
	s.once.Do(func() { close(s.deleted) })
	return deleted, nil
}

// ---------------------------------------------------------------------------
// Not configured / field validation
// ---------------------------------------------------------------------------

func TestOnboardTenant_NotConfiguredWithoutDependencies(t *testing.T) {
	svc := NewTenantOnboardingService(TenantOnboardingDeps{})
	var notConfigured *TenantOnboardingNotConfiguredError
	_, err := svc.OnboardTenant(context.Background(), &OnboardTenantRequest{ID: "acme", AdminUsername: "admin"})
	if !errors.As(err, &notConfigured) {
		t.Fatalf("OnboardTenant without dependencies = %v, want *TenantOnboardingNotConfiguredError", err)
	}
}

func TestOnboardTenant_FieldValidation(t *testing.T) {
	svc := NewTenantOnboardingService(newOnboardingDeps(t))
	ctx := context.Background()

	tests := []struct {
		name string
		req  *OnboardTenantRequest
		want string
	}{
		{"nil request", nil, "id"},
		{"empty id", &OnboardTenantRequest{AdminUsername: "admin"}, "id"},
		{"empty admin username", &OnboardTenantRequest{ID: "acme"}, "admin_username"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var field *TenantOnboardingFieldError
			_, err := svc.OnboardTenant(ctx, tt.req)
			if !errors.As(err, &field) || field.Field != tt.want {
				t.Fatalf("OnboardTenant(%+v) = %v, want *TenantOnboardingFieldError{%s}", tt.req, err, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CSR-mode success
// ---------------------------------------------------------------------------

// TestOnboardTenant_CSRSuccess_CreatesTenantAndAdmin covers the common path:
// an admin CSR, no isolation specified (defaults to prefix), no volume pool.
func TestOnboardTenant_CSRSuccess_CreatesTenantAndAdmin(t *testing.T) {
	ctx := context.Background()
	svc := NewTenantOnboardingService(newOnboardingDeps(t))

	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}

	result, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{
		ID:            "acme",
		Name:          "Acme Corp",
		AdminUsername: "admin",
		AdminCSRPEM:   csrPEM,
	})
	if err != nil {
		t.Fatalf("OnboardTenant: %v", err)
	}
	if result.Tenant == nil || result.Tenant.ID != "acme" || result.Tenant.IsolationType != tenant.IsolationPrefix {
		t.Fatalf("Tenant = %+v, want acme/prefix", result.Tenant)
	}
	if result.Admin.Username != "admin" || result.Admin.UserID == 0 {
		t.Fatalf("Admin = %+v, want admin with a non-zero UserID", result.Admin)
	}
	if len(result.Admin.CertPEM) == 0 || result.Admin.Serial == "" || result.Admin.Thumbprint == "" {
		t.Fatalf("Admin credential missing cert material: %+v", result.Admin)
	}
	if result.Admin.AwaitsBarrierInit {
		t.Fatal("AwaitsBarrierInit = true for a prefix-isolated tenant, want false")
	}
}

// TestOnboardTenant_VolumePoolOptionPlumbedThrough exercises the
// req.VolumePool != "" branch: the no-seal tenant service used by these
// tests ignores CreateTenantOptions outright, so the request still
// succeeds, but the option is constructed and passed down rather than
// skipped.
func TestOnboardTenant_VolumePoolOptionPlumbedThrough(t *testing.T) {
	ctx := context.Background()
	svc := NewTenantOnboardingService(newOnboardingDeps(t))

	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}

	result, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{
		ID:            "acme",
		AdminUsername: "admin",
		AdminCSRPEM:   csrPEM,
		VolumePool:    "pool-1",
	})
	if err != nil {
		t.Fatalf("OnboardTenant with VolumePool set: %v", err)
	}
	if result.Tenant.ID != "acme" {
		t.Fatalf("Tenant.ID = %q, want acme", result.Tenant.ID)
	}
}

// ---------------------------------------------------------------------------
// BYOC-mode success
// ---------------------------------------------------------------------------

// TestOnboardTenant_BYOCSuccess_NoSerialIssued covers the bring-your-own-cert
// path: the admin supplies its own certificate, chaining to the trust
// anchors registered alongside the tenant. Unlike the CSR path, this
// cluster never issued the certificate, so no serial is recorded.
func TestOnboardTenant_BYOCSuccess_NoSerialIssued(t *testing.T) {
	ctx := context.Background()
	svc := NewTenantOnboardingService(newOnboardingDeps(t))

	root := newTestRoot(t, "acme-root")
	leaf := root.clientLeaf(t, "admin", "acme")

	result, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{
		ID:              "acme",
		AdminUsername:   "admin",
		AdminCertPEM:    pemOf(leaf),
		TrustAnchorsPEM: pemOf(root.cert),
	})
	if err != nil {
		t.Fatalf("OnboardTenant: %v", err)
	}
	if result.Admin.Serial != "" {
		t.Fatalf("Admin.Serial = %q, want empty for a bring-your-own certificate", result.Admin.Serial)
	}
	if len(result.Admin.CertPEM) == 0 || result.Admin.Thumbprint == "" {
		t.Fatalf("Admin credential missing cert material: %+v", result.Admin)
	}
}

// ---------------------------------------------------------------------------
// Audit: event shape, gating, and settlement
// ---------------------------------------------------------------------------

// TestOnboardTenant_EmitsAuditRecordOnSuccess pins onboardEvent's shape and
// OnboardTenant's settle(nil) outcome together.
func TestOnboardTenant_EmitsAuditRecordOnSuccess(t *testing.T) {
	ctx := context.Background()
	deps := newOnboardingDeps(t)
	ring := audit.NewRingBuffer(16)
	deps.Audit = ring

	svc := NewTenantOnboardingService(deps)
	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{
		ID: "acme", AdminUsername: "admin", AdminCSRPEM: csrPEM, IsolationType: tenant.IsolationPrefix,
	}); err != nil {
		t.Fatalf("OnboardTenant: %v", err)
	}

	ev := findEvent(ring.Snapshot(), ActionTenantOnboard)
	if ev == nil {
		t.Fatal("no tenant.onboard event recorded")
	}
	if ev.TenantID != "acme" || ev.Resource != "tenant" {
		t.Fatalf("event = %+v, want TenantID=acme Resource=tenant", ev)
	}
	if ev.Outcome != audit.OutcomeSuccess {
		t.Fatalf("event Outcome = %q, want success", ev.Outcome)
	}
	if ev.Attributes["admin_username"] != "admin" {
		t.Fatalf("event admin_username = %v, want admin", ev.Attributes["admin_username"])
	}
	if ev.Attributes["isolation_type"] != string(tenant.IsolationPrefix) {
		t.Fatalf("event isolation_type = %v, want %q", ev.Attributes["isolation_type"], tenant.IsolationPrefix)
	}
}

// TestOnboardTenant_AuditGateFailClosedRefusesBeforeProvisioning proves the
// default audit failure policy (FailClosed) refuses the whole operation
// before bootstrap.ProvisionTenant ever runs: the gating record is part of
// the decision, not a receipt for one already made.
func TestOnboardTenant_AuditGateFailClosedRefusesBeforeProvisioning(t *testing.T) {
	ctx := context.Background()
	deps := newOnboardingDeps(t)
	sentinel := errors.New("audit sink unavailable")
	deps.Audit = &refusingAuditLogger{err: sentinel}
	// deps.AuditPolicy left at its zero value: audit.FailClosed.

	svc := NewTenantOnboardingService(deps)
	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.OnboardTenant(ctx, &OnboardTenantRequest{ID: "acme", AdminUsername: "admin", AdminCSRPEM: csrPEM})
	if err == nil {
		t.Fatal("OnboardTenant with a refusing audit sink under FailClosed = nil error, want a gate refusal")
	}

	if _, getErr := deps.Tenants.GetTenant(ctx, "acme"); getErr == nil {
		t.Fatal("a FailClosed gate refusal must happen before any tenant is created")
	}
}

// TestOnboardTenant_AuditGateFailOpenPermitsDespiteWriteFailure is the
// counterpart: FailOpen tolerates the same write failure and lets
// provisioning proceed.
func TestOnboardTenant_AuditGateFailOpenPermitsDespiteWriteFailure(t *testing.T) {
	ctx := context.Background()
	deps := newOnboardingDeps(t)
	deps.Audit = &refusingAuditLogger{err: errors.New("audit sink unavailable")}
	deps.AuditPolicy = audit.FailOpen

	svc := NewTenantOnboardingService(deps)
	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}

	result, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{ID: "acme", AdminUsername: "admin", AdminCSRPEM: csrPEM})
	if err != nil {
		t.Fatalf("OnboardTenant under FailOpen with a refusing audit sink: %v", err)
	}
	if result.Tenant.ID != "acme" {
		t.Fatalf("Tenant.ID = %q, want acme", result.Tenant.ID)
	}
}

// TestOnboardTenant_SettlesFailureFromProvisioning proves a provisioning
// failure is recorded as a failed onboarding event, keyed by the audit
// component this service names (tenantOnboardingAuditComponent), rather
// than silently dropped.
func TestOnboardTenant_SettlesFailureFromProvisioning(t *testing.T) {
	ctx := context.Background()
	deps := newOnboardingDeps(t)
	ring := audit.NewRingBuffer(16)
	deps.Audit = ring

	svc := NewTenantOnboardingService(deps)
	_, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{
		ID: "acme", AdminUsername: "admin", AdminCSRPEM: []byte("not a csr"),
	})
	if err == nil {
		t.Fatal("OnboardTenant with an unparsable CSR = nil error, want a provisioning failure")
	}

	ev := findEvent(ring.Snapshot(), ActionTenantOnboard)
	if ev == nil {
		t.Fatal("no tenant.onboard event recorded for the failed attempt")
	}
	if ev.Outcome != audit.OutcomeFailure+":onboard-failed" {
		t.Fatalf("event Outcome = %q, want %q", ev.Outcome, audit.OutcomeFailure+":onboard-failed")
	}
}

// TestOnboardTenant_AnswersFromCommittedRecordWhenReadsFail covers a tenant
// read that fails once bootstrap.ProvisionTenant has committed the tenant, as
// a linearizable read does on a loaded cluster. The onboarding has happened:
// it must answer with the record it created and the admin's one-time
// certificate, not with the read's error -- a retry of an exclusive
// provisioning is refused as a duplicate, so that certificate could never be
// delivered again.
func TestOnboardTenant_AnswersFromCommittedRecordWhenReadsFail(t *testing.T) {
	ctx := context.Background()
	deps := newOnboardingDeps(t)
	deps.Tenants = &onboardingFailingGetTenant{TenantServicer: deps.Tenants, err: errors.New("read index not ready")}
	ring := audit.NewRingBuffer(16)
	deps.Audit = ring

	svc := NewTenantOnboardingService(deps)
	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}

	result, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{ID: "acme", Name: "Acme", AdminUsername: "admin", AdminCSRPEM: csrPEM})
	if err != nil {
		t.Fatalf("OnboardTenant = %v, want the committed onboarding answered", err)
	}
	if result.Tenant == nil || result.Tenant.ID != "acme" || result.Tenant.Name != "Acme" {
		t.Fatalf("result.Tenant = %+v, want the created acme record", result.Tenant)
	}
	if len(result.Admin.CertPEM) == 0 {
		t.Fatal("result.Admin.CertPEM is empty, want the admin's one-time certificate")
	}

	ev := findEvent(ring.Snapshot(), ActionTenantOnboard)
	if ev == nil || ev.Outcome != audit.OutcomeSuccess {
		t.Fatalf("event = %+v, want a recorded success outcome", ev)
	}
}

// TestOnboardTenant_RefreshesClientTrustBeforeAnswering pins that a create
// rebuilds this node's client trust once the tenant is committed and before
// it answers, so the handshake that follows the answer admits the admin
// certificate it carries (run 53320: the next request was refused with
// "certificate signed by unknown authority").
func TestOnboardTenant_RefreshesClientTrustBeforeAnswering(t *testing.T) {
	ctx := context.Background()
	deps := newOnboardingDeps(t)
	var calls int
	var committedAtRefresh bool
	deps.ClientTrust = ClientTrustRefresherFunc(func(ctx context.Context) error {
		calls++
		_, err := deps.Tenants.GetTenant(ctx, "acme")
		committedAtRefresh = err == nil
		return nil
	})
	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}

	svc := NewTenantOnboardingService(deps)
	if _, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{
		ID: "acme", AdminUsername: "admin", AdminCSRPEM: csrPEM,
	}); err != nil {
		t.Fatalf("OnboardTenant: %v", err)
	}
	if calls != 1 {
		t.Fatalf("client trust refreshed %d times before the answer, want 1", calls)
	}
	if !committedAtRefresh {
		t.Fatal("client trust refreshed before the tenant was committed, want after")
	}
}

// TestOnboardTenant_ClientTrustRefreshFailureStillAnswers pins that a failed
// rebuild does not turn a committed create into an error: it is logged, and
// the node's periodic rebuild admits the certificate. A create the
// provisioning refused rebuilds nothing.
func TestOnboardTenant_ClientTrustRefreshFailureStillAnswers(t *testing.T) {
	ctx := context.Background()
	deps := newOnboardingDeps(t)
	var logs bytes.Buffer
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	var calls int
	deps.ClientTrust = ClientTrustRefresherFunc(func(context.Context) error {
		calls++
		return errors.New("rebuild failed")
	})
	svc := NewTenantOnboardingService(deps)

	if _, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{
		ID: "refused", AdminUsername: "admin", AdminCSRPEM: []byte("not a csr"),
	}); err == nil {
		t.Fatal("OnboardTenant with an unparsable CSR = nil error, want a provisioning failure")
	}
	if calls != 0 {
		t.Fatalf("a refused create refreshed client trust %d times, want 0", calls)
	}

	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{
		ID: "acme", AdminUsername: "admin", AdminCSRPEM: csrPEM,
	})
	if err != nil {
		t.Fatalf("OnboardTenant with a failing client-trust rebuild: %v, want the committed tenant", err)
	}
	if result.Tenant == nil || result.Tenant.ID != "acme" || len(result.Admin.CertPEM) == 0 {
		t.Fatalf("result = %+v, want the committed tenant and its admin certificate", result)
	}
	if calls != 1 {
		t.Fatalf("client trust refreshed %d times, want 1", calls)
	}
	if out := logs.String(); !strings.Contains(out, "client trust not rebuilt") || !strings.Contains(out, "rebuild failed") {
		t.Fatalf("log = %q, want the failed rebuild reported", out)
	}
}

// TestOnboardTenant_AnswersWithTheCommittedRecord proves the create answers
// with the record its CreateTenant committed and does not read the tenant
// back: a read after the commit spent the request's budget on one more raft
// read, and when it failed it reported a tenant that existed as not created.
func TestOnboardTenant_AnswersWithTheCommittedRecord(t *testing.T) {
	ctx := context.Background()
	deps := newOnboardingDeps(t)
	deps.Tenants = &committedOnlyTenants{TenantServicer: deps.Tenants}
	ring := audit.NewRingBuffer(16)
	deps.Audit = ring

	svc := NewTenantOnboardingService(deps)
	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}

	result, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{ID: "acme", Name: "Acme Corp", AdminUsername: "admin", AdminCSRPEM: csrPEM})
	if err != nil {
		t.Fatalf("OnboardTenant = %v, want the committed record without a read-back", err)
	}
	if result.Tenant == nil || result.Tenant.ID != "acme" || result.Tenant.Name != "Acme Corp" || result.Tenant.CreatedAt.IsZero() {
		t.Fatalf("Tenant = %+v, want the committed acme record", result.Tenant)
	}

	ev := findEvent(ring.Snapshot(), ActionTenantOnboard)
	if ev == nil || ev.Outcome != audit.OutcomeSuccess {
		t.Fatalf("event = %+v, want a recorded success outcome", ev)
	}
}

// onboardingStallingCreate wraps a real tenant.TenantServicer and stalls
// CreateTenant until its context ends, as a tenant-record proposal does while
// the system shard has no leader.
type onboardingStallingCreate struct {
	tenant.TenantServicer
}

func (f *onboardingStallingCreate) CreateTenant(ctx context.Context, _, _ string, _ tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// onboardWithin runs OnboardTenant and fails the test if it does not answer
// within createDeadlineWatchdog.
func onboardWithin(t *testing.T, svc *TenantOnboardingService, ctx context.Context) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{ID: "acme", AdminUsername: "admin", AdminCSRPEM: onboardingCSR(t)})
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(createDeadlineWatchdog):
		t.Fatalf("OnboardTenant did not answer within %v: it is not bounded by the service's deadline", createDeadlineWatchdog)
		return nil
	}
}

// onboardingCSR returns a fresh admin CSR.
func onboardingCSR(t *testing.T) []byte {
	t.Helper()
	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}
	return csrPEM
}

func TestOnboardTenant_StalledConsensus_AnswersWithinServiceDeadline(t *testing.T) {
	deps := newOnboardingDeps(t)
	deps.Tenants = &onboardingStallingCreate{TenantServicer: deps.Tenants}
	deps.Timeout = 50 * time.Millisecond
	ring := audit.NewRingBuffer(16)
	deps.Audit = ring

	err := onboardWithin(t, NewTenantOnboardingService(deps), context.Background())

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the service deadline's context.DeadlineExceeded", err)
	}
	ev := findEvent(ring.Snapshot(), ActionTenantOnboard)
	if ev == nil || ev.Outcome != audit.OutcomeFailure+":onboard-failed" {
		t.Fatalf("event = %+v, want the cut-short onboarding recorded as a failure", ev)
	}
}

func TestOnboardTenant_CallerSoonerDeadlineApplies(t *testing.T) {
	deps := newOnboardingDeps(t)
	deps.Tenants = &onboardingStallingCreate{TenantServicer: deps.Tenants}
	deps.Timeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	err := onboardWithin(t, NewTenantOnboardingService(deps), ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's context.Canceled", err)
	}
}

func TestNewTenantOnboardingService_TimeoutDefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{"default", 0, DefaultTenantOnboardingTimeout},
		{"negative ignored", -time.Second, DefaultTenantOnboardingTimeout},
		{"override", 3 * time.Second, 3 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewTenantOnboardingService(TenantOnboardingDeps{Timeout: tc.timeout})
			if svc.timeout != tc.want {
				t.Fatalf("timeout = %v, want %v", svc.timeout, tc.want)
			}
		})
	}
}

// TestOnboardTenant_AnswersBeforeAStalledAdminStep proves a system-managed
// (auto-unseal) barrier tenant's create answers inside the request's budget
// with the record it committed while the admin's account creation is
// stalled, and that the account is created once the stall clears, after the
// request's context has ended: the admin step runs after the answer, on its
// own bound, never on the request's.
func TestOnboardTenant_AnswersBeforeAStalledAdminStep(t *testing.T) {
	deps := newOnboardingDeps(t)
	users := &stallingAdminUsers{UserServicer: deps.Users, entered: make(chan struct{}), release: make(chan struct{})}
	deps.Users = users
	deps.Tenants = &committedOnlyTenants{TenantServicer: deps.Tenants, autoUnseal: true}
	pending := &signallingPendingAdmins{Store: tenantadmin.NewKVStore(kvstore.NewMemoryKVStore()), deleted: make(chan struct{})}
	deps.Pending = pending
	ring := audit.NewRingBuffer(16)
	deps.Audit = ring

	svc := NewTenantOnboardingService(deps)
	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}

	// The budget every transport gives a tenant create.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	type answer struct {
		result *OnboardTenantResult
		err    error
	}
	answered := make(chan answer, 1)
	go func() {
		result, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{
			ID: "acme", Name: "Acme Corp", IsolationType: tenant.IsolationBarrier, BarrierAutoUnseal: true,
			AdminUsername: "admin", AdminCSRPEM: csrPEM,
		})
		answered <- answer{result, err}
	}()
	var got answer
	select {
	case got = <-answered:
	case <-ctx.Done():
		close(users.release)
		t.Fatal("OnboardTenant did not answer inside the request's budget while the admin step stalled")
	}
	// The request is over: its handler's deferred cancel ends its context.
	cancel()

	if got.err != nil {
		t.Fatalf("OnboardTenant: %v", got.err)
	}
	if got.result.Tenant == nil || got.result.Tenant.ID != "acme" || !got.result.Tenant.BarrierAutoUnseal || got.result.Tenant.IsolationType != tenant.IsolationBarrier {
		t.Fatalf("Tenant = %+v, want the committed acme barrier record", got.result.Tenant)
	}
	if got.result.Admin.AwaitsBarrierInit {
		t.Error("Admin.AwaitsBarrierInit = true, want false: a system-managed barrier awaits no initialization")
	}
	if got.result.Admin.UserID != 0 || len(got.result.Admin.CertPEM) == 0 || got.result.Admin.Thumbprint == "" {
		t.Errorf("Admin = %+v, want the issued certificate and no account id yet", got.result.Admin)
	}
	if ev := findEvent(ring.Snapshot(), ActionTenantOnboard); ev == nil || ev.Outcome != audit.OutcomeSuccess {
		t.Fatalf("event = %+v, want a recorded success outcome", ev)
	}

	select {
	case <-users.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the admin step never started after the create answered")
	}
	close(users.release)
	select {
	case <-pending.deleted:
	case <-time.After(10 * time.Second):
		t.Fatal("the pending admin was not completed after its stalled step was released")
	}
	admin, err := users.UserServicer.GetUserByUsername(context.Background(), "acme", "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername(acme, admin): %v", err)
	}
	if !slices.Contains(admin.RoleIDs, role.SystemRoleID("acme", role.RoleNameAdmin)) {
		t.Fatalf("admin RoleIDs = %v, want the tenant's Admin role", admin.RoleIDs)
	}
}

// recordingPinner records each barrier anchor it is asked to pin.
type recordingPinner struct {
	pins map[clusterseal.Barrier]string
}

func (p *recordingPinner) PinFirstAdmin(_ context.Context, b clusterseal.Barrier, thumbprint string) error {
	if p.pins == nil {
		p.pins = map[clusterseal.Barrier]string{}
	}
	p.pins[b] = thumbprint
	return nil
}

// TestOnboardTenant_PinsABehindBarrierAdminAsTheBarrierAnchor proves the
// TenantOnboardingDeps.BarrierAnchors seam reaches bootstrap: the pending
// first admin of a tenant behind its own barrier is pinned as that barrier's
// epoch-1 anchor, and a prefix tenant, which has no barrier of its own, pins
// nothing.
func TestOnboardTenant_PinsABehindBarrierAdminAsTheBarrierAnchor(t *testing.T) {
	ctx := context.Background()
	deps := newOnboardingDeps(t)
	deps.Pending = tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	pins := &recordingPinner{}
	deps.BarrierAnchors = pins
	svc := NewTenantOnboardingService(deps)

	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}
	isolated, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{
		ID: "isolated", AdminUsername: "admin", AdminCSRPEM: csrPEM, IsolationType: tenant.IsolationVolume,
	})
	if err != nil {
		t.Fatalf("OnboardTenant(isolated): %v", err)
	}
	if _, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{
		ID: "shared", AdminUsername: "admin", AdminCSRPEM: csrPEM, IsolationType: tenant.IsolationPrefix,
	}); err != nil {
		t.Fatalf("OnboardTenant(shared): %v", err)
	}
	want := map[clusterseal.Barrier]string{clusterseal.TenantBarrier("isolated"): isolated.Admin.Thumbprint}
	if isolated.Admin.Thumbprint == "" || !maps.Equal(pins.pins, want) {
		t.Fatalf("pinned anchors = %v, want %v", pins.pins, want)
	}
}

// countingSigner is a firstboot.CertSigner that signs through a real signer
// and records the tenant of every CSR it was handed, so a test can tell which
// of the two configured signers the onboarding path actually used.
type countingSigner struct {
	inner   firstboot.CertSigner
	tenants []string
}

func (s *countingSigner) SignAdminCSR(ctx context.Context, tenantID, subject string, csrPEM []byte) ([]byte, error) {
	s.tenants = append(s.tenants, tenantID)
	return s.inner.SignAdminCSR(ctx, tenantID, subject, csrPEM)
}

// TestOnboardTenant_PendingAdminSignerSignsBehindBarrierAdmins proves the
// TenantOnboardingDeps.PendingAdminSigner seam reaches bootstrap: an admin
// whose tenant keeps its IAM behind its own barrier is signed by it, and a
// prefix tenant's admin is not.
func TestOnboardTenant_PendingAdminSignerSignsBehindBarrierAdmins(t *testing.T) {
	ctx := context.Background()
	deps := newOnboardingDeps(t)
	deps.Pending = tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	pending := &countingSigner{inner: &bootstrap.KeyBSigner{Certs: deps.Certs}}
	deps.PendingAdminSigner = pending
	svc := NewTenantOnboardingService(deps)

	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id        string
		isolation tenant.IsolationType
	}{
		{"isolated", tenant.IsolationVolume},
		{"shared", tenant.IsolationPrefix},
	} {
		result, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{
			ID: tc.id, AdminUsername: "admin", AdminCSRPEM: csrPEM, IsolationType: tc.isolation,
		})
		if err != nil {
			t.Fatalf("OnboardTenant(%s): %v", tc.id, err)
		}
		if len(result.Admin.CertPEM) == 0 {
			t.Fatalf("OnboardTenant(%s) returned no admin certificate", tc.id)
		}
	}
	if !slices.Equal(pending.tenants, []string{"isolated"}) {
		t.Fatalf("PendingAdminSigner signed for tenants %v, want only [isolated]", pending.tenants)
	}
}

// TestOnboardTenant_SelfManagedCSRWithoutPendingAdminSignerRefused: a node
// with no cluster CA key composes no PendingAdminSigner, and a self-managed
// isolated tenant's Key B is sealed at create, so its first-admin CSR is
// refused as system first boot refuses one, before the tenant exists.
func TestOnboardTenant_SelfManagedCSRWithoutPendingAdminSignerRefused(t *testing.T) {
	ctx := context.Background()
	deps := newOnboardingDeps(t)
	deps.Pending = tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	deps.PendingAdminSigner = nil
	svc := NewTenantOnboardingService(deps)

	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.OnboardTenant(ctx, &OnboardTenantRequest{
		ID: "isolated", AdminUsername: "admin", AdminCSRPEM: csrPEM, IsolationType: tenant.IsolationVolume,
	})
	var requiresCA *ceremony.AdminCredentialRequiresCAError
	if !errors.As(err, &requiresCA) {
		t.Fatalf("OnboardTenant = %v, want *ceremony.AdminCredentialRequiresCAError", err)
	}
	if _, err := deps.Tenants.GetTenant(ctx, "isolated"); err == nil {
		t.Fatal("the tenant exists after a refused onboarding, want it never created")
	}
}

// TestOnboardTenant_SystemManagedCSRWithoutPendingAdminSignerSignsWithKeyB: on
// a node with no cluster CA key, a system-managed barrier serves from create,
// so its pending admin's CSR is signed by the tenant's own Key B CA.
func TestOnboardTenant_SystemManagedCSRWithoutPendingAdminSignerSignsWithKeyB(t *testing.T) {
	ctx := context.Background()
	deps := newOnboardingDeps(t)
	deps.Pending = tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	deps.PendingAdminSigner = nil
	svc := NewTenantOnboardingService(deps)

	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{
		ID: "isolated", AdminUsername: "admin", AdminCSRPEM: csrPEM,
		IsolationType: tenant.IsolationVolume, BarrierAutoUnseal: true,
	})
	if err != nil {
		t.Fatalf("OnboardTenant: %v", err)
	}
	if len(result.Admin.CertPEM) == 0 {
		t.Fatal("OnboardTenant returned no admin certificate")
	}
	provider := deps.Certs.(*cert.DefaultCertProvider)
	caPEMs, err := provider.CACertPEMs(ctx)
	if err != nil {
		t.Fatalf("CACertPEMs: %v", err)
	}
	admin := parseOnboardingPEMCert(t, result.Admin.CertPEM)
	var issuer *x509.Certificate
	for _, caPEM := range caPEMs {
		if ca := parseOnboardingPEMCert(t, caPEM); admin.CheckSignatureFrom(ca) == nil {
			issuer = ca
			break
		}
	}
	if issuer == nil {
		t.Fatal("admin certificate is signed by no registered Key B CA")
	}
	if ok, err := provider.IsTenantIssuer(ctx, "isolated", issuer); err != nil || !ok {
		t.Fatalf("IsTenantIssuer(isolated) = %v, %v; want the admin certificate signed by the tenant's own Key B CA", ok, err)
	}
}

// parseOnboardingPEMCert parses the first certificate in pemBytes.
func parseOnboardingPEMCert(t *testing.T, pemBytes []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("no PEM block")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return c
}

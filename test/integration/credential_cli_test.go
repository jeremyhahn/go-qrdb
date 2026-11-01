// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

//go:build integration

// credential_cli_test.go drives the `qrdb credential` COMMAND TREE (submit,
// store, retrieve, list, strategy, delete, rotate, versions,
// retrieve-version, gc) through the real client CLI binary (cmd/qrdb),
// against every transport (REST, gRPC, QUIC, MCP, Unix) sharing the
// multi_protocol_helpers_test.go harness. This closes the CLI-coverage-audit
// finding that every one of these subcommands had ZERO integration coverage
// of any kind. The versioned named-credential rotation API (pkg/credentials,
// exposed via sdk/go/transport.CredentialClient) is qrdb-native across all
// five transports and defaults to the manual (in-memory) strategy on the
// shared harness node -- see pkg/credentials/config.go's StrategyManual --
// so every case in this file is a real positive-path assertion.
//
// `credential retrieve`/RetrieveCredential blocks under the manual strategy
// until the name is submitted (pkg/credentials/service.go's
// retrieveManual), EXCEPT for the documented fast path when the value
// already exists. Every retrieve in this file is therefore always issued
// strictly after a submit/store/rotate of the same name so it never blocks.
package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	qrdbnode "github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// credentialVersionCLIJSON mirrors transport.CredentialVersionInfo.
type credentialVersionCLIJSON struct {
	Version        uint64 `json:"version"`
	IsCurrent      bool   `json:"is_current"`
	CreatedAtUnix  int64  `json:"created_at_unix,omitempty"`
	GraceUntilUnix int64  `json:"grace_until_unix,omitempty"`
}

// credentialVersionsCLIJSON mirrors transport.CredentialListVersionsResponse.
type credentialVersionsCLIJSON struct {
	Name     string                     `json:"name"`
	Versions []credentialVersionCLIJSON `json:"versions"`
}

// credentialRotateCLIJSON mirrors transport.CredentialRotateResponse.
type credentialRotateCLIJSON struct {
	Name            string `json:"name"`
	PreviousVersion uint64 `json:"previous_version"`
	NewVersion      uint64 `json:"new_version"`
	GraceUntilUnix  int64  `json:"grace_until_unix,omitempty"`
}

// TestCredential_CLI_Lifecycle_PerProtocol drives the full `qrdb credential`
// lifecycle -- submit, store, retrieve, list, strategy, rotate, versions,
// retrieve-version, gc, delete -- through the CLI binary against every
// transport sharing one multiProtoNode.
func TestCredential_CLI_Lifecycle_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testCredentialLifecycleOverProtocol(t, node, proto)
		})
	}
}

// credentialGrants are the ten credentials:* verbs a tenant's credential role
// holds.
var credentialGrants = []string{
	"credentials:submit", "credentials:strategy", "credentials:list",
	"credentials:delete", "credentials:rotate", "credentials:gc_expired",
	"credentials:retrieve_version", "credentials:list_versions",
	"credentials:store", "credentials:retrieve",
}

// credentialTenants is a node's tenant fixture for the tenant-scoped
// named-credential store: two barrier tenants whose principals hold every
// credentials:* verb, a barrier tenant whose principal holds none, and a
// prefix tenant, on the shared system barrier, whose principal holds them all.
type credentialTenants struct {
	acme, globex, ungranted, shared string
	conns                           map[string][2]string // tenant -> cert, key
}

func newCredentialTenants(t *testing.T, node *multiProtoNode) *credentialTenants {
	t.Helper()
	ct := &credentialTenants{
		acme: "cred-acme", globex: "cred-globex", ungranted: "cred-initech", shared: "cred-shared",
		conns: make(map[string][2]string),
	}
	creator := multiProtocols[0]
	for _, id := range []string{ct.acme, ct.globex, ct.ungranted} {
		createTenant(t, node, creator, id, "Credential Tenant "+id, "barrier", true)
	}
	createTenant(t, node, creator, ct.shared, "Credential Tenant "+ct.shared, "prefix", false)
	for _, id := range []string{ct.acme, ct.globex, ct.shared} {
		cert, key := provisionTenantIdentity(t, node, id, "credential-keeper", credentialGrants)
		ct.conns[id] = [2]string{cert, key}
	}
	cert, key := provisionTenantIdentity(t, node, ct.ungranted, "credential-outsider", []string{"admin:health"})
	ct.conns[ct.ungranted] = [2]string{cert, key}
	return ct
}

func (ct *credentialTenants) conn(node *multiProtoNode, proto multiProtocol, tenantID string) []string {
	c := ct.conns[tenantID]
	return tenantCLIArgsForProto(node, proto, c[0], c[1])
}

// TestCredential_CLI_TenantStores_PerProtocol drives the tenant-scoped
// named-credential store end to end over every network transport: a tenant
// principal holding credentials:* reads and writes its own tenant's store,
// which no other tenant (and not the system tenant) can see; a principal
// without the grants is refused; and a tenant on the shared system barrier,
// which has no barrier of its own to keep a store under, is refused rather
// than handed the node-wide store.
func TestCredential_CLI_TenantStores_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t)
	tenants := newCredentialTenants(t, node)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testCredentialTenantStoresOverProtocol(t, node, tenants, proto)
		})
	}
}

func testCredentialTenantStoresOverProtocol(t *testing.T, node *multiProtoNode, ct *credentialTenants, proto multiProtocol) {
	t.Helper()
	acme, globex := ct.conn(node, proto, ct.acme), ct.conn(node, proto, ct.globex)
	run := func(conn []string, args ...string) string {
		t.Helper()
		out, err := runQRDB(t, node.cli, conn, args...)
		if err != nil {
			t.Fatalf("[%s] %v: %v\noutput: %s", proto.name, args, err, out)
		}
		return strings.TrimSpace(out)
	}
	list := func(conn []string) []string {
		t.Helper()
		out := run(conn, "--output", "json", "credential", "list")
		var names []string
		if err := json.Unmarshal([]byte(out), &names); err != nil {
			t.Fatalf("[%s] decode credential list JSON: %v\noutput: %s", proto.name, err, out)
		}
		return names
	}
	// The same name in two tenants is two credentials.
	shared := "tenant-api-key-" + proto.name
	acmeOnly := "acme-only-" + proto.name
	run(acme, "credential", "store", shared, "--value", "acme-value")
	run(globex, "credential", "store", shared, "--value", "globex-value")
	run(acme, "credential", "store", acmeOnly, "--value", "acme-private")

	if got := run(acme, "credential", "retrieve", shared); got != "acme-value" {
		t.Fatalf("[%s] acme retrieve %s = %q, want %q", proto.name, shared, got, "acme-value")
	}
	if got := run(globex, "credential", "retrieve", shared); got != "globex-value" {
		t.Fatalf("[%s] globex retrieve %s = %q, want %q", proto.name, shared, got, "globex-value")
	}
	if got := run(acme, "credential", "strategy"); !strings.Contains(got, "barrier") {
		t.Fatalf("[%s] a tenant's store must be barrier-strategy, got %q", proto.name, got)
	}

	// globex can neither list nor read acme's credential, and the system
	// tenant's node-wide store holds neither tenant's.
	if names := list(globex); stringSliceContains(names, acmeOnly) || !stringSliceContains(names, shared) {
		t.Fatalf("[%s] globex's list = %v, want %q and not %q", proto.name, names, shared, acmeOnly)
	}
	if out, err := runQRDB(t, node.cli, globex, "credential", "retrieve", acmeOnly); err == nil || strings.Contains(out, "acme-private") {
		t.Fatalf("[%s] globex must not read acme's %s: err=%v output: %s", proto.name, acmeOnly, err, out)
	}
	if !stringSliceContains(list(acme), acmeOnly) {
		t.Fatalf("[%s] acme's list must name its own %s", proto.name, acmeOnly)
	}
	sysOut, err := runCLIProto(t, node, proto, "--output", "json", "credential", "list")
	if err != nil {
		t.Fatalf("[%s] system credential list: %v\noutput: %s", proto.name, err, sysOut)
	}
	if strings.Contains(sysOut, shared) || strings.Contains(sysOut, acmeOnly) {
		t.Fatalf("[%s] the system store must hold no tenant credential, got: %s", proto.name, sysOut)
	}

	// A rotation in acme versions acme's credential only.
	run(acme, "credential", "rotate", shared, "--value", "acme-value-2")
	if got := run(acme, "credential", "retrieve", shared); got != "acme-value-2" {
		t.Fatalf("[%s] acme retrieve after rotate = %q, want %q", proto.name, got, "acme-value-2")
	}
	if got := run(globex, "credential", "retrieve", shared); got != "globex-value" {
		t.Fatalf("[%s] globex's %s changed with acme's rotation: %q", proto.name, shared, got)
	}

	// globex deleting the shared name leaves acme's in place.
	run(globex, "credential", "delete", shared)
	if got := run(acme, "credential", "retrieve", shared); got != "acme-value-2" {
		t.Fatalf("[%s] acme's %s went with globex's delete: %q", proto.name, shared, got)
	}

	// A principal without the grants is refused by authorization.
	assertCrossTenantIAMForbidden(t, node, proto, ct.conn(node, proto, ct.ungranted), "",
		"credential", "list")

	// A tenant on the shared system barrier is refused, not served the
	// node-wide store.
	out, err := runQRDB(t, node.cli, ct.conn(node, proto, ct.shared), "credential", "store", shared, "--value", "leak")
	if err == nil {
		t.Fatalf("[%s] a shared-barrier tenant must be refused a credential store, got success: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), "dedicated barrier") {
		t.Fatalf("[%s] a shared-barrier tenant's refusal must name the missing dedicated barrier, got: %s", proto.name, out)
	}
	if strings.Contains(run(acme, "credential", "retrieve", shared), "leak") {
		t.Fatalf("[%s] the shared-barrier tenant's write reached acme's store", proto.name)
	}
}

// TestCredential_Embedded_TenantStores is TestCredential_CLI_TenantStores_PerProtocol
// over the embedded transport, which has no CLI: the SDK client of an
// in-process node with authorization enabled, called as each tenant's own
// principal.
func TestCredential_Embedded_TenantStores(t *testing.T) {
	cfg := homeNodeConfig(t)
	// Unlike the home suite, every call here is authorized.
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(true)
	n, err := qrdbnode.NewStandaloneNode(context.Background(), cfg, nil, homeNodeOptions()...)
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	if n.IsSealed() {
		t.Fatal("node is sealed")
	}
	pools, ok := n.BarrierRegistry().VolumeService()
	if !ok {
		t.Fatal("the node attached no volume catalog")
	}
	if _, err := pools.CreatePool(context.Background(), volume.CreatePoolRequest{Name: "default"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}

	const acme, globex, initech, shared = "ecred-acme", "ecred-globex", "ecred-initech", "ecred-shared"
	onboardCredentialTenant(t, n, acme, tenant.IsolationBarrier)
	onboardCredentialTenant(t, n, globex, tenant.IsolationBarrier)
	onboardCredentialTenant(t, n, initech, tenant.IsolationBarrier)
	onboardCredentialTenant(t, n, shared, tenant.IsolationPrefix)
	grants := make([]permission.Permission, 0, len(credentialGrants))
	for _, g := range credentialGrants {
		grants = append(grants, permission.Permission(g))
	}
	acmeCtx := embeddedCredentialPrincipal(t, n, acme, grants)
	globexCtx := embeddedCredentialPrincipal(t, n, globex, grants)
	initechCtx := embeddedCredentialPrincipal(t, n, initech, []permission.Permission{permission.AdminHealth})
	sharedCtx := embeddedCredentialPrincipal(t, n, shared, grants)
	creds := n.Client().Credentials()

	retrieve := func(ctx context.Context, name string) (string, error) {
		t.Helper()
		buf, err := creds.CredentialRetrieveBuffer(ctx, name)
		if err != nil {
			return "", err
		}
		value, err := service.CredentialWireValue(buf)
		return string(value), err
	}
	mustRetrieve := func(ctx context.Context, name, want string) {
		t.Helper()
		got, err := retrieve(ctx, name)
		if err != nil || got != want {
			t.Fatalf("retrieve %s = %q, %v; want %q", name, got, err, want)
		}
	}
	names := func(ctx context.Context) []string {
		t.Helper()
		resp, err := creds.CredentialList(ctx, &transport.CredentialListRequest{})
		if err != nil {
			t.Fatalf("CredentialList: %v", err)
		}
		return resp.Names
	}

	const sharedName, acmeOnly = "tenant-api-key", "acme-only"
	for _, step := range []struct {
		ctx         context.Context
		name, value string
	}{{acmeCtx, sharedName, "acme-value"}, {globexCtx, sharedName, "globex-value"}, {acmeCtx, acmeOnly, "acme-private"}} {
		if err := creds.CredentialStore(step.ctx, step.name, []byte(step.value)); err != nil {
			t.Fatalf("CredentialStore(%s): %v", step.name, err)
		}
	}
	mustRetrieve(acmeCtx, sharedName, "acme-value")
	mustRetrieve(globexCtx, sharedName, "globex-value")
	if strategy, err := creds.CredentialStrategy(acmeCtx); err != nil || strategy.Strategy != "barrier" {
		t.Fatalf("a tenant's store must be barrier-strategy, got %+v, %v", strategy, err)
	}

	if got := names(globexCtx); stringSliceContains(got, acmeOnly) || !stringSliceContains(got, sharedName) {
		t.Fatalf("globex's list = %v, want %q and not %q", got, sharedName, acmeOnly)
	}
	if got, err := retrieve(globexCtx, acmeOnly); err == nil || got == "acme-private" {
		t.Fatalf("globex must not read acme's %s: %q, %v", acmeOnly, got, err)
	}

	if _, err := creds.CredentialRotate(acmeCtx, &transport.CredentialRotateRequest{Name: sharedName, NewValue: []byte("acme-value-2")}); err != nil {
		t.Fatalf("CredentialRotate: %v", err)
	}
	mustRetrieve(acmeCtx, sharedName, "acme-value-2")
	mustRetrieve(globexCtx, sharedName, "globex-value")
	if err := creds.CredentialDelete(globexCtx, sharedName); err != nil {
		t.Fatalf("CredentialDelete: %v", err)
	}
	mustRetrieve(acmeCtx, sharedName, "acme-value-2")

	// A principal without the grants is refused by authorization.
	if _, err := creds.CredentialList(initechCtx, &transport.CredentialListRequest{}); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "permission") {
		t.Fatalf("a principal without credentials:list must be refused a permission, got %v", err)
	}

	// A tenant on the shared system barrier is refused, not served the
	// node-wide store.
	err = creds.CredentialStore(sharedCtx, sharedName, []byte("leak"))
	if err == nil || !strings.Contains(err.Error(), "dedicated barrier") {
		t.Fatalf("a shared-barrier tenant must be refused for want of a dedicated barrier, got %v", err)
	}
	mustRetrieve(acmeCtx, sharedName, "acme-value-2")
}

// onboardCredentialTenant onboards tenantID with the given isolation; a
// dedicated barrier is auto-unsealed so its store is reachable at once.
func onboardCredentialTenant(t *testing.T, n *qrdbnode.Node, tenantID string, isolation tenant.IsolationType) {
	t.Helper()
	_, csrPEM, err := certtest.NewCSR(tenantID + "-admin")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}
	if _, err := n.Services().TenantOnboarding.OnboardTenant(context.Background(), &service.OnboardTenantRequest{
		ID:                tenantID,
		Name:              tenantID,
		IsolationType:     isolation,
		BarrierAutoUnseal: isolation != tenant.IsolationPrefix,
		AdminUsername:     "admin",
		AdminCSRPEM:       csrPEM,
	}); err != nil {
		t.Fatalf("OnboardTenant(%s, %s): %v", tenantID, isolation, err)
	}
}

// embeddedCredentialPrincipal creates a role holding grants and a user
// holding it in tenantID, and returns a context authenticated as that user
// in its own tenant.
func embeddedCredentialPrincipal(t *testing.T, n *qrdbnode.Node, tenantID string, grants []permission.Permission) context.Context {
	t.Helper()
	ctx := tenant.WithTenantID(context.Background(), tenantID)
	svc := n.Services()
	r, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name: "credential-principal", TenantID: tenantID, Permissions: grants,
	})
	if err != nil {
		t.Fatalf("CreateRole(%s): %v", tenantID, err)
	}
	u, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "credential-principal",
		TenantID:             tenantID,
		RoleIDs:              []string{r.ID},
	})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", tenantID, err)
	}
	identity := &auth.Identity{Subject: u.Username, TenantID: tenantID, Roles: []string{r.ID}}
	identity.BindUser(u.ID, u.Username)
	return auth.WithIdentity(ctx, identity)
}

func testCredentialLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	base := fmt.Sprintf("clc-%s-%d", proto.name, time.Now().UnixNano())
	submittedName := base + "-submitted"
	storedName := base + "-stored"

	// --- strategy ----------------------------------------------------------
	// cmd/qrdbd/seal_subsystem.go defaults CredentialStrategy to "barrier"
	// whenever the seal subsystem is enabled (as it always is on this
	// harness) and no explicit seal.credential_strategy override is
	// configured -- "manual" is only the pkg/credentials package-level
	// default for callers that construct a bare CredentialService with no
	// Config at all. Confirmed empirically against a live server.
	out, err := runCLIProto(t, node, proto, "credential", "strategy")
	if err != nil {
		t.Fatalf("[%s] credential strategy: %v\noutput: %s", proto.name, err, out)
	}
	strategy := strings.TrimSpace(out)
	if strategy != "barrier" && strategy != "manual" {
		t.Fatalf("[%s] credential strategy = %q, want %q or %q", proto.name, strategy, "barrier", "manual")
	}

	// --- submit + retrieve (fast path: retrieve issued strictly after
	// submit of the same name, so RetrieveCredential's blocking-waiter path
	// is never exercised) -----------------------------------------------
	out, err = runCLIProto(t, node, proto, "credential", "submit", submittedName, "--value", "submitted-value-1")
	if err != nil {
		t.Fatalf("[%s] credential submit: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] credential submit: expected OK, got: %s", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "credential", "retrieve", submittedName)
	if err != nil {
		t.Fatalf("[%s] credential retrieve (submitted): %v\noutput: %s", proto.name, err, out)
	}
	if strings.TrimSpace(out) != "submitted-value-1" {
		t.Fatalf("[%s] credential retrieve (submitted) = %q, want %q", proto.name, strings.TrimSpace(out), "submitted-value-1")
	}

	// A second submit of the same name is strategy-dependent:
	// SubmitCredential's one-shot-claim semantics (ErrCredentialAlreadySubmitted)
	// are specific to the manual strategy (pkg/credentials/service.go); for
	// every other strategy (including "barrier", this harness's default),
	// SubmitCredential is defined to delegate straight to StoreCredential and
	// therefore always overwrites, just like `credential store`.
	out, err = runCLIProto(t, node, proto, "credential", "submit", submittedName, "--value", "submitted-value-2")
	if strategy == "manual" {
		if err == nil {
			t.Fatalf("[%s] credential submit (re-submit, manual strategy) should fail, got: %s", proto.name, out)
		}
	} else {
		if err != nil {
			t.Fatalf("[%s] credential submit (re-submit, %s strategy) should overwrite, got: %v\noutput: %s", proto.name, strategy, err, out)
		}
		out, err = runCLIProto(t, node, proto, "credential", "retrieve", submittedName)
		if err != nil {
			t.Fatalf("[%s] credential retrieve (post-resubmit): %v\noutput: %s", proto.name, err, out)
		}
		if strings.TrimSpace(out) != "submitted-value-2" {
			t.Fatalf("[%s] credential retrieve (post-resubmit) = %q, want %q", proto.name, strings.TrimSpace(out), "submitted-value-2")
		}
	}

	// --- store + retrieve ----------------------------------------------
	out, err = runCLIProto(t, node, proto, "credential", "store", storedName, "--value", "stored-value-1")
	if err != nil {
		t.Fatalf("[%s] credential store: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] credential store: expected OK, got: %s", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "credential", "retrieve", storedName)
	if err != nil {
		t.Fatalf("[%s] credential retrieve (stored): %v\noutput: %s", proto.name, err, out)
	}
	if strings.TrimSpace(out) != "stored-value-1" {
		t.Fatalf("[%s] credential retrieve (stored) = %q, want %q", proto.name, strings.TrimSpace(out), "stored-value-1")
	}
	// store always overwrites, unlike submit.
	out, err = runCLIProto(t, node, proto, "credential", "store", storedName, "--value", "stored-value-1-overwritten")
	if err != nil {
		t.Fatalf("[%s] credential store (overwrite): %v\noutput: %s", proto.name, err, out)
	}
	out, err = runCLIProto(t, node, proto, "credential", "retrieve", storedName)
	if err != nil {
		t.Fatalf("[%s] credential retrieve (post-overwrite): %v\noutput: %s", proto.name, err, out)
	}
	if strings.TrimSpace(out) != "stored-value-1-overwritten" {
		t.Fatalf("[%s] credential retrieve (post-overwrite) = %q, want %q", proto.name, strings.TrimSpace(out), "stored-value-1-overwritten")
	}

	// --- list ------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "credential", "list")
	if err != nil {
		t.Fatalf("[%s] credential list: %v\noutput: %s", proto.name, err, out)
	}
	var names []string
	if jerr := json.Unmarshal([]byte(out), &names); jerr != nil {
		t.Fatalf("[%s] decode credential list JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if !stringSliceContains(names, submittedName) || !stringSliceContains(names, storedName) {
		t.Fatalf("[%s] credential list = %v, want to contain %q and %q", proto.name, names, submittedName, storedName)
	}

	// --- rotate (with a short grace period so the previous version stays
	// retrievable, then expires for the gc assertion below) ----------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "credential", "rotate", storedName,
		"--value", "stored-value-2",
		"--grace-period", "15",
	)
	if err != nil {
		t.Fatalf("[%s] credential rotate: %v\noutput: %s", proto.name, err, out)
	}
	var rotated credentialRotateCLIJSON
	if jerr := json.Unmarshal([]byte(out), &rotated); jerr != nil {
		t.Fatalf("[%s] decode credential rotate JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if rotated.Name != storedName || rotated.PreviousVersion != 1 || rotated.NewVersion != 2 || rotated.GraceUntilUnix == 0 {
		t.Fatalf("[%s] credential rotate result = %+v, want name=%q previous_version=1 new_version=2 grace_until_unix>0", proto.name, rotated, storedName)
	}

	// --- versions --------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "credential", "versions", storedName)
	if err != nil {
		t.Fatalf("[%s] credential versions: %v\noutput: %s", proto.name, err, out)
	}
	var versions credentialVersionsCLIJSON
	if jerr := json.Unmarshal([]byte(out), &versions); jerr != nil {
		t.Fatalf("[%s] decode credential versions JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if versions.Name != storedName || len(versions.Versions) != 2 {
		t.Fatalf("[%s] credential versions result = %+v, want name=%q with 2 entries (v1 grace + v2 current)", proto.name, versions, storedName)
	}
	v1, ok1 := findCredentialVersion(versions.Versions, 1)
	v2, ok2 := findCredentialVersion(versions.Versions, 2)
	if !ok1 || v1.IsCurrent || v1.GraceUntilUnix == 0 {
		t.Fatalf("[%s] credential versions v1 = %+v (found=%v), want is_current=false grace_until_unix>0", proto.name, v1, ok1)
	}
	if !ok2 || !v2.IsCurrent {
		t.Fatalf("[%s] credential versions v2 = %+v (found=%v), want is_current=true", proto.name, v2, ok2)
	}

	// --- retrieve-version (v1 still within its grace window) ---------------
	out, err = runCLIProto(t, node, proto, "credential", "retrieve-version", storedName, "--version", "1")
	if err != nil {
		t.Fatalf("[%s] credential retrieve-version (v1, within grace): %v\noutput: %s", proto.name, err, out)
	}
	if strings.TrimSpace(out) != "stored-value-1-overwritten" {
		t.Fatalf("[%s] credential retrieve-version (v1) = %q, want %q", proto.name, strings.TrimSpace(out), "stored-value-1-overwritten")
	}
	// version 0 (the default, omitted) means "current".
	out, err = runCLIProto(t, node, proto, "credential", "retrieve-version", storedName)
	if err != nil {
		t.Fatalf("[%s] credential retrieve-version (current): %v\noutput: %s", proto.name, err, out)
	}
	if strings.TrimSpace(out) != "stored-value-2" {
		t.Fatalf("[%s] credential retrieve-version (current) = %q, want %q", proto.name, strings.TrimSpace(out), "stored-value-2")
	}

	// --- gc: reap v1 once its grace period elapses. The grace period is
	// generous (well beyond what a single CLI round trip needs) because each
	// runCLIProto call spawns a brand-new OS process and (re)dials the
	// transport from scratch -- QUIC's handshake in particular can push the
	// rotate -> versions -> retrieve-version(v1) sequence close to a couple
	// of seconds of real wall-clock time before gc even runs. gc reaps
	// nothing while v1 is still in grace, so it is retried until it reaps v1,
	// bounded by a deadline past the grace period. ------------------------
	var reaped int
	if !pollUntil(30*time.Second, time.Second, func() bool {
		out, err = runCLIProto(t, node, proto, "credential", "gc", storedName)
		if err != nil {
			t.Fatalf("[%s] credential gc: %v\noutput: %s", proto.name, err, out)
		}
		reaped = parseReapedCount(t, proto, out)
		return reaped >= 1
	}) {
		t.Fatalf("[%s] credential gc reaped = %d, want >= 1 after the grace period elapsed", proto.name, reaped)
	}

	// v1 must now be gone: expired versions are permanently denied even if
	// the underlying storage hasn't been swept yet (ErrVersionExpired).
	out, err = runCLIProto(t, node, proto, "credential", "retrieve-version", storedName, "--version", "1")
	if err == nil {
		t.Fatalf("[%s] credential retrieve-version (v1, post-gc) should fail, got: %s", proto.name, out)
	}

	// --- delete must wipe an in-grace (not yet expired) historical
	// version too, not just the current value and already-expired
	// history. Rotate once more so storedName has a fresh v3 current plus
	// a v2 that is still inside its grace window (long enough that it
	// cannot expire before this test asserts against it), then confirm
	// delete removes both. This is the blocker scenario: prior to the fix,
	// DeleteCredential(barrier) only removed the current value, leaving
	// the #meta sidecar and #v2 history retrievable via
	// retrieve-version/versions after "delete" reported success. ---------
	out, err = runCLIProto(t, node, proto, "--output", "json", "credential", "rotate", storedName,
		"--value", "stored-value-3",
		"--grace-period", "120",
	)
	if err != nil {
		t.Fatalf("[%s] credential rotate (pre-delete, in-grace): %v\noutput: %s", proto.name, err, out)
	}
	var rotatedAgain credentialRotateCLIJSON
	if jerr := json.Unmarshal([]byte(out), &rotatedAgain); jerr != nil {
		t.Fatalf("[%s] decode credential rotate JSON (pre-delete): %v\noutput: %s", proto.name, jerr, out)
	}
	if rotatedAgain.PreviousVersion != 2 || rotatedAgain.NewVersion != 3 {
		t.Fatalf("[%s] credential rotate (pre-delete) result = %+v, want previous_version=2 new_version=3", proto.name, rotatedAgain)
	}
	out, err = runCLIProto(t, node, proto, "credential", "retrieve-version", storedName, "--version", "2")
	if err != nil {
		t.Fatalf("[%s] credential retrieve-version (v2, within grace, pre-delete): %v\noutput: %s", proto.name, err, out)
	}
	if strings.TrimSpace(out) != "stored-value-2" {
		t.Fatalf("[%s] credential retrieve-version (v2, pre-delete) = %q, want %q", proto.name, strings.TrimSpace(out), "stored-value-2")
	}

	// --- delete ----------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "credential", "delete", submittedName)
	if err != nil {
		t.Fatalf("[%s] credential delete (submitted): %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] credential delete (submitted): expected OK, got: %s", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "credential", "delete", storedName)
	if err != nil {
		t.Fatalf("[%s] credential delete (stored): %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] credential delete (stored): expected OK, got: %s", proto.name, out)
	}

	// The blocker assertion: the in-grace v2 history and the deleted
	// current (v3) value must both be gone immediately, not merely
	// unreachable once some later GC runs.
	out, err = runCLIProto(t, node, proto, "credential", "retrieve-version", storedName, "--version", "2")
	if err == nil {
		t.Fatalf("[%s] credential retrieve-version (v2, post-delete) should fail, got: %s", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "credential", "retrieve-version", storedName, "--version", "3")
	if err == nil {
		t.Fatalf("[%s] credential retrieve-version (v3/current, post-delete) should fail, got: %s", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "credential", "versions", storedName)
	if err == nil {
		t.Fatalf("[%s] credential versions (post-delete) should fail, got: %s", proto.name, out)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "credential", "list")
	if err != nil {
		t.Fatalf("[%s] credential list (post-delete): %v\noutput: %s", proto.name, err, out)
	}
	var namesAfterDelete []string
	if jerr := json.Unmarshal([]byte(out), &namesAfterDelete); jerr != nil {
		t.Fatalf("[%s] decode credential list JSON (post-delete): %v\noutput: %s", proto.name, jerr, out)
	}
	if stringSliceContains(namesAfterDelete, submittedName) || stringSliceContains(namesAfterDelete, storedName) {
		t.Fatalf("[%s] credential list (post-delete) = %v, want neither %q nor %q", proto.name, namesAfterDelete, submittedName, storedName)
	}
}

// findCredentialVersion returns the entry with the given version number.
func findCredentialVersion(versions []credentialVersionCLIJSON, version uint64) (credentialVersionCLIJSON, bool) {
	for _, v := range versions {
		if v.Version == version {
			return v, true
		}
	}
	return credentialVersionCLIJSON{}, false
}

// parseReapedCount extracts the integer count from `credential gc`'s fixed
// "reaped %d version(s)\n" output (newCredentialGCCmd always prints this
// form regardless of --output).
func parseReapedCount(t *testing.T, proto multiProtocol, out string) int {
	t.Helper()
	fields := strings.Fields(out)
	for i, f := range fields {
		if f == "reaped" && i+1 < len(fields) {
			n, err := strconv.Atoi(fields[i+1])
			if err != nil {
				t.Fatalf("[%s] credential gc: could not parse reaped count from output: %s", proto.name, out)
			}
			return n
		}
	}
	t.Fatalf("[%s] credential gc: output did not contain a 'reaped N' count: %s", proto.name, out)
	return 0
}

// credentialRotationPolicyCLIJSON is `qrdb credential rotation-policy get
// --output json`.
type credentialRotationPolicyCLIJSON struct {
	Enabled        bool   `json:"enabled"`
	Interval       string `json:"interval"`
	RetainVersions int    `json:"retain_versions"`
}

// TestCredential_CLI_Cluster_OneStore_PerProtocol is the property of
// docs/credentials/architecture.md, "Storage": a cluster keeps one credential
// store, on the system shard, so a credential, its rotation policy and its
// version history written through one node are the same records read
// through every other node.
func TestCredential_CLI_Cluster_OneStore_PerProtocol(t *testing.T) {
	t.Parallel()
	nodes, _ := startMultiProtocolCluster(t, 0)

	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			testCredentialOneStoreOverProtocol(t, nodes, proto)
		})
	}
}

func testCredentialOneStoreOverProtocol(t *testing.T, nodes [3]*multiProtoNode, proto multiProtocol) {
	t.Helper()
	a, b, c := nodes[0], nodes[1], nodes[2]
	name := fmt.Sprintf("ccl-%s-%d", proto.name, time.Now().UnixNano())

	// --- a credential stored through A is read through B and C ------------
	if out, err := runCLIProto(t, a, proto, "credential", "store", name, "--value", "cluster-value-1"); err != nil {
		t.Fatalf("[%s] credential store on A: %v\noutput: %s", proto.name, err, out)
	}
	for label, node := range map[string]*multiProtoNode{"B": b, "C": c} {
		out, err := runCLIProto(t, node, proto, "credential", "retrieve", name)
		if err != nil {
			t.Fatalf("[%s] credential retrieve on %s: %v\noutput: %s", proto.name, label, err, out)
		}
		if got := strings.TrimSpace(out); got != "cluster-value-1" {
			t.Fatalf("[%s] credential retrieve on %s = %q, want %q", proto.name, label, got, "cluster-value-1")
		}
		out, err = runCLIProto(t, node, proto, "--output", "json", "credential", "list")
		if err != nil {
			t.Fatalf("[%s] credential list on %s: %v\noutput: %s", proto.name, label, err, out)
		}
		var names []string
		if jerr := json.Unmarshal([]byte(out), &names); jerr != nil {
			t.Fatalf("[%s] decode credential list JSON on %s: %v\noutput: %s", proto.name, label, jerr, out)
		}
		if !stringSliceContains(names, name) {
			t.Fatalf("[%s] credential list on %s = %v, want to contain %q", proto.name, label, names, name)
		}
	}

	// --- a rotation policy set through A is the policy B reads -------------
	if out, err := runCLIProto(t, a, proto, "credential", "rotation-policy", "set", name,
		"--enabled", "--interval", "720h", "--retain-versions", "3"); err != nil {
		t.Fatalf("[%s] rotation-policy set on A: %v\noutput: %s", proto.name, err, out)
	}
	out, err := runCLIProto(t, b, proto, "--output", "json", "credential", "rotation-policy", "get", name)
	if err != nil {
		t.Fatalf("[%s] rotation-policy get on B: %v\noutput: %s", proto.name, err, out)
	}
	var policy credentialRotationPolicyCLIJSON
	if jerr := json.Unmarshal([]byte(out), &policy); jerr != nil {
		t.Fatalf("[%s] decode rotation-policy JSON on B: %v\noutput: %s", proto.name, jerr, out)
	}
	interval, perr := time.ParseDuration(policy.Interval)
	if !policy.Enabled || perr != nil || interval != 720*time.Hour || policy.RetainVersions != 3 {
		t.Fatalf("[%s] rotation-policy on B = %+v, want enabled interval=720h retain_versions=3", proto.name, policy)
	}

	// --- a rotation through B is the version history A and C read ----------
	if out, err := runCLIProto(t, b, proto, "credential", "rotate", name,
		"--value", "cluster-value-2", "--grace-period", "120"); err != nil {
		t.Fatalf("[%s] credential rotate on B: %v\noutput: %s", proto.name, err, out)
	}
	out, err = runCLIProto(t, a, proto, "--output", "json", "credential", "versions", name)
	if err != nil {
		t.Fatalf("[%s] credential versions on A: %v\noutput: %s", proto.name, err, out)
	}
	var versions credentialVersionsCLIJSON
	if jerr := json.Unmarshal([]byte(out), &versions); jerr != nil {
		t.Fatalf("[%s] decode credential versions JSON on A: %v\noutput: %s", proto.name, jerr, out)
	}
	v2, ok := findCredentialVersion(versions.Versions, 2)
	if len(versions.Versions) != 2 || !ok || !v2.IsCurrent {
		t.Fatalf("[%s] credential versions on A = %+v, want v1 in grace and v2 current", proto.name, versions)
	}
	out, err = runCLIProto(t, c, proto, "credential", "retrieve", name)
	if err != nil {
		t.Fatalf("[%s] credential retrieve on C after rotate: %v\noutput: %s", proto.name, err, out)
	}
	if got := strings.TrimSpace(out); got != "cluster-value-2" {
		t.Fatalf("[%s] credential retrieve on C after rotate = %q, want %q", proto.name, got, "cluster-value-2")
	}

	// --- a delete through C is gone on A -----------------------------------
	if out, err := runCLIProto(t, c, proto, "credential", "delete", name); err != nil {
		t.Fatalf("[%s] credential delete on C: %v\noutput: %s", proto.name, err, out)
	}
	if out, err := runCLIProto(t, a, proto, "credential", "retrieve", name); err == nil {
		t.Fatalf("[%s] credential retrieve on A after delete on C should fail, got: %s", proto.name, out)
	}
}

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

// auth_factor_condition_cli_test.go covers the conditional factor add — the
// one-time claim (--if-no-active-type) and the expected factor-set version
// (--expected-version) — end to end on every transport: the real qrdb CLI
// against a self-spawned qrdbd over REST, gRPC, QUIC, MCP and Unix, and the
// embedded transport through sdk/go/node's in-process client, which the CLI
// cannot select (see cli_multiprotocol_test.go). Each asserts the condition
// holding, the condition failing with the refusal the server carried, the
// version a list reports driving a later add, and that of N adds racing one
// claim exactly one succeeds.
package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// factorConditionRacers is how many adds race one claim in each concurrency
// assertion.
const factorConditionRacers = 6

// authFactorAddConditionCLIJSON is `qrdb auth factor add --output json`: the
// factor record with factor_set_version inline beside it.
type authFactorAddConditionCLIJSON struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	FactorSetVersion uint64 `json:"factor_set_version"`
}

// authFactorListVersionCLIJSON is the part of `qrdb auth factor list
// --output json` a conditional add depends on.
type authFactorListVersionCLIJSON struct {
	Factors          []authFactorRecordCLIJSON `json:"factors"`
	FactorSetVersion uint64                    `json:"factor_set_version"`
}

// randomCredentialHex returns a credential no other test enrols, so a lookup
// key collision can never stand in for a refused condition.
func randomCredentialHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

func TestAuthFactorAdd_Conditional_CLI_PerProtocol(t *testing.T) {
	mp := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			testConditionalFactorAddOverProtocol(t, mp, proto)
		})
	}
}

func testConditionalFactorAddOverProtocol(t *testing.T, mp *multiProtoNode, proto multiProtocol) {
	t.Helper()

	out, err := runCLIProto(t, mp, proto, "--output", "json", "users", "create", "--without-email-login-key",
		"--username", fmt.Sprintf("afc-%s-%d", proto.name, time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("[%s] users create (fixture): %v\noutput: %s", proto.name, err, out)
	}
	var u userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &u); jerr != nil {
		t.Fatalf("[%s] decode users create JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	userID := fmt.Sprintf("%d", u.ID)

	add := func(factorType string, extra ...string) (string, error) {
		args := append([]string{"--output", "json", "auth", "factor", "add", "--user", userID,
			"--type", factorType, "--label", "afc", "--credential-hex", randomCredentialHex(t)}, extra...)
		return runCLIProto(t, mp, proto, args...)
	}

	// --- the one-time claim holds, then refuses the second ----------------
	out, err = add("piv", "--if-no-active-type")
	if err != nil {
		t.Fatalf("[%s] first conditional add: %v\noutput: %s", proto.name, err, out)
	}
	var claimed authFactorAddConditionCLIJSON
	if jerr := json.Unmarshal([]byte(out), &claimed); jerr != nil {
		t.Fatalf("[%s] decode conditional add JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if claimed.ID == "" || claimed.Type != "piv" || claimed.FactorSetVersion != 1 {
		t.Fatalf("[%s] conditional add = %+v, want a piv record at factor_set_version 1", proto.name, claimed)
	}

	out, err = add("piv", "--if-no-active-type")
	if err == nil {
		t.Fatalf("[%s] second conditional add succeeded, want it refused: %s", proto.name, out)
	}
	wantType := "user already holds an active piv factor " + claimed.ID
	if !strings.Contains(out, wantType) {
		t.Fatalf("[%s] refused add output = %q, want it to contain %q", proto.name, out, wantType)
	}

	// --- the version a list reports drives the next add -------------------
	out, err = runCLIProto(t, mp, proto, "--output", "json", "auth", "factor", "list", "--user", userID)
	if err != nil {
		t.Fatalf("[%s] auth factor list: %v\noutput: %s", proto.name, err, out)
	}
	var listed authFactorListVersionCLIJSON
	if jerr := json.Unmarshal([]byte(out), &listed); jerr != nil {
		t.Fatalf("[%s] decode auth factor list JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if listed.FactorSetVersion != 1 || len(listed.Factors) != 1 {
		t.Fatalf("[%s] list = %+v, want one factor at factor_set_version 1", proto.name, listed)
	}

	out, err = add("piv", "--expected-version", "0")
	if err == nil {
		t.Fatalf("[%s] add at a stale version succeeded: %s", proto.name, out)
	}
	if want := "factor-set version is 1, expected 0"; !strings.Contains(out, want) {
		t.Fatalf("[%s] stale add output = %q, want it to contain %q", proto.name, out, want)
	}

	// The version condition says nothing about type: a second piv factor is
	// accepted at the version the list reported.
	out, err = add("piv", "--expected-version", "1")
	if err != nil {
		t.Fatalf("[%s] add at the listed version: %v\noutput: %s", proto.name, err, out)
	}
	var versioned authFactorAddConditionCLIJSON
	if jerr := json.Unmarshal([]byte(out), &versioned); jerr != nil || versioned.FactorSetVersion != 2 {
		t.Fatalf("[%s] add at version 1 = %+v (%v), want factor_set_version 2\noutput: %s", proto.name, versioned, jerr, out)
	}

	// --- N racing claims on a type the user does not hold: one wins -------
	outs := make([]string, factorConditionRacers)
	errs := make([]error, factorConditionRacers)
	var wg sync.WaitGroup
	for i := range factorConditionRacers {
		wg.Go(func() { outs[i], errs[i] = add("fido2", "--if-no-active-type") })
	}
	wg.Wait()

	var winners int
	for i, e := range errs {
		switch {
		case e == nil:
			winners++
		case !strings.Contains(outs[i], "user already holds an active fido2 factor"):
			t.Errorf("[%s] racer %d failed for a reason other than the claim: %v\noutput: %s", proto.name, i, e, outs[i])
		}
	}
	if winners != 1 {
		t.Fatalf("[%s] %d of %d racing claims succeeded, want exactly 1", proto.name, winners, factorConditionRacers)
	}

	out, err = runCLIProto(t, mp, proto, "--output", "json", "auth", "factor", "list", "--user", userID)
	if err != nil {
		t.Fatalf("[%s] auth factor list (after race): %v\noutput: %s", proto.name, err, out)
	}
	listed = authFactorListVersionCLIJSON{}
	if jerr := json.Unmarshal([]byte(out), &listed); jerr != nil {
		t.Fatalf("[%s] decode auth factor list JSON (after race): %v\noutput: %s", proto.name, jerr, out)
	}
	if got := countFactorsOfType(listed.Factors, "fido2"); got != 1 || listed.FactorSetVersion != 3 {
		t.Fatalf("[%s] after the race: %d fido2 factors at version %d, want 1 at version 3", proto.name, got, listed.FactorSetVersion)
	}
}

func countFactorsOfType(factors []authFactorRecordCLIJSON, factorType string) int {
	var n int
	for _, f := range factors {
		if f.Type == factorType && f.RevokedAt == nil {
			n++
		}
	}
	return n
}

// startEmbeddedFactorNode boots an in-process standalone node serving no
// network transport and returns it with a context bound to an identity
// granted user_authfactors:add and :list -- the embedded transport's own
// authentication model, as startEmbeddedAdminNode's is -- and the id of a
// user to enrol factors against.
func startEmbeddedFactorNode(t *testing.T) (*node.Node, context.Context, uint64) {
	t.Helper()

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := node.NewStandaloneNode(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	svc := n.Services()
	enroller, err := svc.Role.CreateRole(context.Background(), &role.CreateRoleRequest{
		Name:        "auth-factors-embedded-enroller",
		TenantID:    tenantid.System,
		Permissions: []permission.Permission{permission.UserAuthFactorsAdd, permission.UserAuthFactorsList},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if _, err := svc.User.CreateUser(context.Background(), &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "auth-factors-embedded-enroller",
		TenantID:             tenantid.System,
		RoleIDs:              []string{enroller.ID},
	}); err != nil {
		t.Fatalf("CreateUser (enroller): %v", err)
	}
	owner, err := svc.User.CreateUser(context.Background(), &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "auth-factors-embedded-owner",
		TenantID:             tenantid.System,
	})
	if err != nil {
		t.Fatalf("CreateUser (owner): %v", err)
	}

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  "auth-factors-embedded-enroller",
		TenantID: tenantid.System,
		Roles:    []string{enroller.ID},
	})
	return n, ctx, owner.ID
}

func TestAuthFactorAdd_Conditional_Embedded(t *testing.T) {
	n, ctx, userID := startEmbeddedFactorNode(t)
	factors := n.Client().UserAuthFactors()
	if factors == nil {
		t.Fatal("embedded client has no UserAuthFactors surface")
	}

	addReq := func(factorType string) *transport.AuthFactorAddRequest {
		cred := make([]byte, 16)
		if _, err := rand.Read(cred); err != nil {
			t.Fatalf("rand: %v", err)
		}
		return &transport.AuthFactorAddRequest{UserID: userID, Type: factorType, Label: "afc", Credential: cred}
	}

	// --- the one-time claim holds, then refuses the second ----------------
	req := addReq("piv")
	req.IfNoActiveType = true
	first, err := factors.AddAuthFactor(ctx, req)
	if err != nil {
		t.Fatalf("first conditional add: %v", err)
	}
	if first.FactorSetVersion != 1 {
		t.Fatalf("first conditional add version = %d, want 1", first.FactorSetVersion)
	}

	req = addReq("piv")
	req.IfNoActiveType = true
	_, err = factors.AddAuthFactor(ctx, req)
	if !errors.Is(err, sdk.ErrFactorConditionFailed) {
		t.Fatalf("second conditional add err = %v, want sdk.ErrFactorConditionFailed", err)
	}
	refusal, ok := errors.AsType[*sdk.FactorConditionFailedError](err)
	if !ok || refusal.Condition != sdk.FactorConditionNoActiveOfType || refusal.CurrentVersion != 1 || refusal.ExistingFactorID == "" {
		t.Fatalf("refusal = %+v, want a no-active-of-type refusal at version 1 naming the holder", refusal)
	}

	// --- the version a list reports drives the next add -------------------
	listed, err := factors.ListAuthFactors(ctx, &transport.AuthFactorListRequest{UserID: userID})
	if err != nil || listed.FactorSetVersion != 1 {
		t.Fatalf("list = %+v, %v; want factor-set version 1", listed, err)
	}

	stale := uint64(0)
	req = addReq("piv")
	req.ExpectedFactorSetVersion = &stale
	_, err = factors.AddAuthFactor(ctx, req)
	refusal, ok = errors.AsType[*sdk.FactorConditionFailedError](err)
	if !ok || refusal.Condition != sdk.FactorConditionExpectedVersion || refusal.ExpectedVersion != 0 || refusal.CurrentVersion != 1 {
		t.Fatalf("stale add err = %v, want an expected-version refusal 0 vs 1", err)
	}

	req = addReq("piv")
	req.ExpectedFactorSetVersion = &listed.FactorSetVersion
	resp, err := factors.AddAuthFactor(ctx, req)
	if err != nil || resp.FactorSetVersion != 2 {
		t.Fatalf("add at the listed version = %+v, %v; want version 2", resp, err)
	}

	// --- N racing claims on a type the user does not hold: one wins -------
	errs := make([]error, factorConditionRacers)
	var wg sync.WaitGroup
	for i := range factorConditionRacers {
		race := addReq("fido2")
		race.IfNoActiveType = true
		wg.Go(func() { _, errs[i] = factors.AddAuthFactor(ctx, race) })
	}
	wg.Wait()

	var winners int
	for i, e := range errs {
		switch {
		case e == nil:
			winners++
		case !errors.Is(e, sdk.ErrFactorConditionFailed):
			t.Errorf("racer %d failed for a reason other than the claim: %v", i, e)
		}
	}
	if winners != 1 {
		t.Fatalf("%d of %d racing claims succeeded, want exactly 1", winners, factorConditionRacers)
	}
}

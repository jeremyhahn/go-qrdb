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

package sdk_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// This file is deliberately in the EXTERNAL test package and imports ONLY
// sdk/go. That is what makes it a consumer test: it exercises the identity
// surface exactly as go-xkms and go-quicbaas must reach it, so a field or
// constant that exists in pkg/user but was never re-exported fails to compile
// here rather than being discovered by a consumer at bump time.

func TestSDK_UserCarriesEveryIdentityField(t *testing.T) {
	u := sdk.User{
		ID:          42,
		Username:    "operator",
		Email:       "operator@example.com",
		Name:        "Ops Operator",
		TenantID:    "tenant-1",
		RoleIDs:     []string{"viewer"},
		GroupIDs:    []string{"platform"},
		AuthMethods: []string{"webauthn"},
		Lifecycle:   sdk.LifecycleActive,
		Attributes:  map[string]string{"team": "platform"},
	}
	if !u.CanAuthenticate() {
		t.Fatal("an active user reported unable to authenticate")
	}
	if u.LastAuth != nil {
		t.Fatal("LastAuth should default to nil")
	}
	u.Lifecycle = sdk.LifecycleSuspended
	if u.CanAuthenticate() {
		t.Fatal("a suspended user reported able to authenticate")
	}
}

func TestSDK_LifecycleConstantsAndTransitions(t *testing.T) {
	states := []sdk.Lifecycle{
		sdk.LifecyclePending,
		sdk.LifecycleActive,
		sdk.LifecycleSuspended,
		sdk.LifecycleLocked,
		sdk.LifecycleDisabled,
		sdk.LifecycleDeleting,
	}
	for _, s := range states {
		if !s.Valid() {
			t.Errorf("%q is not valid through the SDK re-export", s)
		}
	}

	// The guarantee a consumer's deletion path depends on.
	if sdk.LifecycleDeleting.CanTransitionTo(sdk.LifecycleActive) {
		t.Error("deleting -> active was admitted through the SDK")
	}
	if !sdk.LifecycleDeleting.CanTransitionTo(sdk.LifecycleDeleting) {
		t.Error("re-asserting deleting must stay a no-op so a delete is resumable")
	}
	if !sdk.LifecycleActive.CanTransitionTo(sdk.LifecycleSuspended) {
		t.Error("active -> suspended was refused through the SDK")
	}
}

func TestSDK_LifecycleErrorsAreMatchable(t *testing.T) {
	// A consumer must be able to tell "you may not" from "the store failed"
	// without string matching.
	var refused error = &sdk.LifecycleTransitionError{
		ID:   7,
		From: sdk.LifecycleDeleting,
		To:   sdk.LifecycleActive,
	}
	var te *sdk.LifecycleTransitionError
	if !errors.As(refused, &te) || te.ID != 7 {
		t.Fatalf("LifecycleTransitionError is not matchable through the SDK: %v", refused)
	}

	var invalid error = &sdk.InvalidLifecycleError{Value: "quarantined", Op: "create"}
	var ile *sdk.InvalidLifecycleError
	if !errors.As(invalid, &ile) || ile.Value != "quarantined" {
		t.Fatalf("InvalidLifecycleError is not matchable through the SDK: %v", invalid)
	}
}

func TestSDK_IdentitySetNormalizersAreTheCanonicalOnes(t *testing.T) {
	// Case is PRESERVED for group ids (opaque minted tokens) and FOLDED for
	// auth methods (a closed protocol vocabulary). A consumer that normalizes
	// its own copy differently disagrees with the store about what the user is.
	groups, err := sdk.NormalizeGroupIDs([]string{" Ops ", "ops", "Ops"})
	if err != nil {
		t.Fatalf("NormalizeGroupIDs: %v", err)
	}
	if len(groups) != 2 || groups[0] != "Ops" || groups[1] != "ops" {
		t.Fatalf("NormalizeGroupIDs: got %v, want [Ops ops]", groups)
	}

	methods, err := sdk.NormalizeAuthMethods([]string{"WebAuthn", "webauthn", " PIV "})
	if err != nil {
		t.Fatalf("NormalizeAuthMethods: %v", err)
	}
	if len(methods) != 2 || methods[0] != "webauthn" || methods[1] != "piv" {
		t.Fatalf("NormalizeAuthMethods: got %v, want [webauthn piv]", methods)
	}

	if _, err := sdk.NormalizeGroupIDs([]string{""}); err == nil {
		t.Fatal("a blank group id was accepted")
	}
}

func TestSDK_RequestsCarryTheNewFields(t *testing.T) {
	// Compile-time proof that a consumer can create and update every field
	// through the SDK alone — this is the surface that lets go-quicbaas delete
	// its BrokerProfile side-record.
	create := sdk.CreateUserRequest{
		Username:    "invitee",
		Email:       "invitee@example.com",
		Lifecycle:   sdk.LifecyclePending,
		GroupIDs:    []string{"platform"},
		AuthMethods: []string{"webauthn"},
	}
	if create.Lifecycle != sdk.LifecyclePending {
		t.Fatal("CreateUserRequest.Lifecycle did not round-trip")
	}

	update := sdk.UpdateUserRequest{
		ID:          42,
		Lifecycle:   sdk.LifecycleDeleting,
		GroupIDs:    []string{},
		AuthMethods: []string{"piv"},
	}
	if update.Lifecycle != sdk.LifecycleDeleting || update.LastAuth != nil {
		t.Fatal("UpdateUserRequest did not round-trip")
	}
	// The empty non-nil slice is the CLEAR signal, distinct from nil. A
	// consumer revoking every membership depends on the two staying different.
	if update.GroupIDs == nil {
		t.Fatal("an empty GroupIDs must stay non-nil; clearing is not the same as leaving alone")
	}
}

// The remote transport DTO must carry every identity field too, or a consumer
// on REST/gRPC/QUIC/MCP/unix reads a user that is missing exactly the fields
// this change exists to give it.
func TestSDK_TransportUserCarriesEveryIdentityField(t *testing.T) {
	u := transport.User{
		ID:          42,
		Username:    "operator",
		Email:       "operator@example.com",
		TenantID:    "tenant-1",
		Lifecycle:   string(sdk.LifecycleActive),
		GroupIDs:    []string{"platform"},
		AuthMethods: []string{"webauthn"},
		LastAuth:    "2026-09-01T12:00:00Z",
	}

	// It also has to survive a JSON round trip on the wire, which is where an
	// `omitempty` on a "clear" field would quietly destroy the semantics.
	data, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back transport.User
	if uerr := json.Unmarshal(data, &back); uerr != nil {
		t.Fatalf("unmarshal: %v", uerr)
	}
	if back.Lifecycle != u.Lifecycle || len(back.GroupIDs) != 1 ||
		len(back.AuthMethods) != 1 || back.LastAuth != u.LastAuth {
		t.Fatalf("round trip lost fields: %+v", back)
	}
}

// An update request must encode the three-way distinction: nil leaves a set
// alone, an empty non-nil slice clears it. `omitempty` would drop the empty
// slice from the body and make a revocation silently do nothing, so its absence
// on these two fields is load-bearing and pinned here.
func TestSDK_TransportUpdateRequestEncodesClearVersusLeaveAlone(t *testing.T) {
	clear, err := json.Marshal(transport.UpdateUserRequest{ID: 1, GroupIDs: []string{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(clear, []byte(`"group_ids":[]`)) {
		t.Fatalf("a cleared GroupIDs must encode as []; got %s", clear)
	}

	leave, err := json.Marshal(transport.UpdateUserRequest{ID: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(leave, []byte(`"group_ids":null`)) {
		t.Fatalf("an untouched GroupIDs must encode as null; got %s", leave)
	}
}

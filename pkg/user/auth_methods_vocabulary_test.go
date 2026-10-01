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

package user

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// A permitted-method policy is written in qrdb's vocabulary or not at all. A
// misspelled token ("webauth", "PIV-card") used to be stored as written and
// then matched nothing, so the policy silently refused the method it was
// written to allow. It is refused at write time instead, with a typed error
// that is also a ValidationError, so every transport answers it as a bad
// argument.
func TestNormalizeAuthMethods_RefusesNamesOutsideTheVocabulary(t *testing.T) {
	for _, bad := range [][]string{{"totp"}, {auth.MethodMTLS, "webauth"}, {"fido2"}, {"password"}} {
		_, err := NormalizeAuthMethods(bad)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("NormalizeAuthMethods(%q) err = %v, want a *ValidationError", bad, err)
		}
	}

	got, err := NormalizeAuthMethods([]string{" MTLS ", "WebAuthn", "piv", "mtls"})
	if err != nil {
		t.Fatalf("known methods refused: %v", err)
	}
	if want := []string{auth.MethodMTLS, auth.MethodWebAuthn, auth.MethodPIV}; !slices.Equal(got, want) {
		t.Errorf("NormalizeAuthMethods = %v, want %v", got, want)
	}
	if got, err := NormalizeAuthMethods(nil); got != nil || err != nil {
		t.Errorf("nil policy = %v, %v; want nil, nil (no restriction)", got, err)
	}
}

// Every write path refuses an unknown name: CreateUser and UpdateUser are the
// only two, and every transport and the SDK reach the store through them.
func TestAuthMethods_UnknownNameRefusedOnEveryWritePath(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)

	_, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "ada", Email: "ada@example.com", TenantID: "t1",
		AuthMethods: []string{auth.MethodPIV, "totp"},
	})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("CreateUser with an unknown method: err = %v, want a *ValidationError", err)
	}

	u := createTestUser(t, svc, "bob", "t1")
	_, err = svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, AuthMethods: []string{"totp"}})
	if !errors.As(err, &ve) {
		t.Fatalf("UpdateUser with an unknown method: err = %v, want a *ValidationError", err)
	}
	got, gerr := svc.GetUser(ctx, u.ID)
	if gerr != nil {
		t.Fatalf("GetUser: %v", gerr)
	}
	if len(got.AuthMethods) != 0 {
		t.Errorf("the refused update was written: AuthMethods = %v", got.AuthMethods)
	}
}

// Failing closed on WRITE must not lock out a record written before the
// vocabulary was enforced. A stored policy holding a name qrdb does not know
// is read back unchanged, and the account stays updatable on every other
// field: validation applies to a policy being written, never to one merely
// carried along.
func TestAuthMethods_LegacyStoredPolicyStaysReadableAndUpdatable(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	u := createTestUser(t, svc, "carol", "t1")

	// Plant the pre-enforcement record directly: this is what an older build
	// wrote, and no API can write it now.
	raw, err := kv.Get(ctx, primaryKey(u.ID))
	if err != nil {
		t.Fatalf("raw Get: %v", err)
	}
	var rec User
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	legacy := []string{"legacy-otp", auth.MethodMTLS}
	rec.AuthMethods = legacy
	planted, err := json.Marshal(&rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := kv.Put(ctx, primaryKey(u.ID), planted); err != nil {
		t.Fatalf("raw Put: %v", err)
	}

	got, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser on a legacy policy: %v", err)
	}
	if !slices.Equal(got.AuthMethods, legacy) {
		t.Fatalf("AuthMethods read back as %v, want %v unchanged", got.AuthMethods, legacy)
	}

	updated, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Name: "Carol"})
	if err != nil {
		t.Fatalf("an update that does not touch the policy was refused: %v", err)
	}
	if updated.Name != "Carol" || !slices.Equal(updated.AuthMethods, legacy) {
		t.Errorf("update = name %q methods %v, want Carol and the legacy policy kept", updated.Name, updated.AuthMethods)
	}
}

// An admin editing a legacy policy may KEEP an entry the vocabulary no longer
// knows — dropping it silently would change what the account may do — but may
// not ADD a new unknown one.
func TestAuthMethods_EditingALegacyPolicyKeepsButDoesNotAdd(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	u := createTestUser(t, svc, "dave", "t1")
	raw, err := kv.Get(ctx, primaryKey(u.ID))
	if err != nil {
		t.Fatalf("raw Get: %v", err)
	}
	var rec User
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rec.AuthMethods = []string{"legacy-otp", auth.MethodMTLS}
	planted, err := json.Marshal(&rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := kv.Put(ctx, primaryKey(u.ID), planted); err != nil {
		t.Fatalf("raw Put: %v", err)
	}

	// Keep the legacy entry, swap mtls for piv: allowed.
	updated, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, AuthMethods: []string{" Legacy-OTP ", auth.MethodPIV}})
	if err != nil {
		t.Fatalf("keeping a legacy entry was refused: %v", err)
	}
	if want := []string{"legacy-otp", auth.MethodPIV}; !slices.Equal(updated.AuthMethods, want) {
		t.Fatalf("AuthMethods = %v, want %v", updated.AuthMethods, want)
	}

	// Add a second unknown entry: refused, and nothing is written.
	_, err = svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, AuthMethods: []string{"legacy-otp", "new-otp"}})
	var unknown *UnknownAuthMethodError
	if !errors.As(err, &unknown) || unknown.Method != "new-otp" {
		t.Fatalf("adding an unknown entry: err = %v, want *UnknownAuthMethodError naming new-otp", err)
	}
	got, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if want := []string{"legacy-otp", auth.MethodPIV}; !slices.Equal(got.AuthMethods, want) {
		t.Errorf("the refused update was written: AuthMethods = %v", got.AuthMethods)
	}

	// Once dropped, the legacy entry cannot come back: it is no longer stored.
	if _, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, AuthMethods: []string{auth.MethodPIV}}); err != nil {
		t.Fatalf("dropping the legacy entry: %v", err)
	}
	if _, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, AuthMethods: []string{"legacy-otp"}}); !errors.As(err, &unknown) {
		t.Fatalf("re-adding a dropped legacy entry: err = %v, want a refusal", err)
	}
}

// The refusal is typed: it names the token, and it is a ValidationError by
// unwrapping, which is what every transport maps to a bad argument.
func TestUnknownAuthMethodError_IsTypedAndAValidationError(t *testing.T) {
	_, err := NormalizeAuthMethods([]string{auth.MethodPIV, " WebAuth "})
	var unknown *UnknownAuthMethodError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %T %v, want *UnknownAuthMethodError", err, err)
	}
	if unknown.Method != "webauth" || unknown.Index != 1 {
		t.Errorf("refusal = %+v, want the normalized token webauth at index 1", unknown)
	}
	want := `user: unknown authentication method "webauth": not a built-in method or an authenticator in this node's chain`
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "authMethods" {
		t.Errorf("unwrapped = %+v, want a ValidationError on authMethods", ve)
	}
}

// WithAuthMethodVocabulary scopes the vocabulary to the store: a store told
// its node can authenticate "oauth" accepts it, a store without the option
// (built-ins only) refuses it, and the package-level NormalizeAuthMethods —
// which has no node — stays on the built-ins. known is consulted at write
// time, so a vocabulary that widens after construction (a late-bound
// middleware) is honoured.
func TestWithAuthMethodVocabulary_ScopesTheStore(t *testing.T) {
	ctx := context.Background()
	policy := []string{auth.MethodMTLS, "oauth"}

	var chainReady bool
	known := func(m string) bool { return auth.IsBuiltinAuthMethod(m) || (chainReady && m == "oauth") }
	scoped, err := NewStore(newMemKVStore(), newTestIDGen(t), WithAuthMethodVocabulary(known))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	var unknown *UnknownAuthMethodError
	_, err = scoped.CreateUser(ctx, &CreateUserRequest{Username: "a", Email: "a@example.com", TenantID: "t1", AuthMethods: policy})
	if !errors.As(err, &unknown) {
		t.Fatalf("before the chain is known: err = %v, want a refusal", err)
	}
	chainReady = true
	if _, err := scoped.CreateUser(ctx, &CreateUserRequest{Username: "a", Email: "a@example.com", TenantID: "t1", AuthMethods: policy}); err != nil {
		t.Fatalf("with oauth in the store's vocabulary: %v", err)
	}

	plain := newTestStore(t)
	if _, err := plain.CreateUser(ctx, &CreateUserRequest{Username: "b", Email: "b@example.com", TenantID: "t1", AuthMethods: policy}); !errors.As(err, &unknown) {
		t.Fatalf("a store without the option accepted oauth: err = %v", err)
	}
	if _, err := NormalizeAuthMethods(policy); !errors.As(err, &unknown) {
		t.Fatalf("NormalizeAuthMethods (no node) accepted oauth: err = %v", err)
	}
}

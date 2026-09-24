// Copyright (c) 2026 Jeremy Hahn
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

package user

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// fakeFactorLister is a controllable FactorLister double: it returns exactly
// the factors it was constructed with, or an error when errAfter is set.
// There is no in-process real UserAuthFactorServicer usable from pkg/user
// without an import cycle (pkg/service imports pkg/user), so this is the
// package's own boundary double for the interface it declares.
type fakeFactorLister struct {
	factors []*auth.AuthFactor
	err     error
}

func (f *fakeFactorLister) List(_ context.Context, _ uint64) ([]*auth.AuthFactor, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.factors, nil
}

func activeFactor(id, factorType string, registeredAt time.Time) *auth.AuthFactor {
	return &auth.AuthFactor{ID: id, Type: auth.AuthFactorType(factorType), RegisteredAt: registeredAt}
}

func revokedFactor(id, factorType string, registeredAt time.Time) *auth.AuthFactor {
	now := time.Now().UTC()
	return &auth.AuthFactor{ID: id, Type: auth.AuthFactorType(factorType), RegisteredAt: registeredAt, RevokedAt: &now}
}

// countingFactorLister counts List calls, so a test can assert a projection
// reads the factor store exactly once.
type countingFactorLister struct {
	fakeFactorLister
	calls int
}

func (c *countingFactorLister) List(ctx context.Context, userID uint64) ([]*auth.AuthFactor, error) {
	c.calls++
	return c.fakeFactorLister.List(ctx, userID)
}

func TestResolveFactorView_NilListerIsZero(t *testing.T) {
	view, err := ResolveFactorView(context.Background(), nil, 1)
	if err != nil {
		t.Fatalf("ResolveFactorView(nil lister) error = %v, want nil", err)
	}
	if view.Factors != nil || view.MFAEnabled || view.MFAMethods != nil || view.MFAEnrolledAt != "" {
		t.Errorf("ResolveFactorView(nil lister) = %+v, want the zero FactorView", view)
	}
}

// TestResolveFactorView_ListErrorPropagates asserts a factor-store failure is
// an error, never an account reported as having no second factor.
func TestResolveFactorView_ListErrorPropagates(t *testing.T) {
	storeErr := errors.New("store unavailable")
	view, err := ResolveFactorView(context.Background(), &fakeFactorLister{err: storeErr}, 7)
	var lookupErr *FactorLookupError
	if !errors.As(err, &lookupErr) {
		t.Fatalf("ResolveFactorView(List error) error = %v, want *FactorLookupError", err)
	}
	if lookupErr.UserID != 7 || !errors.Is(err, storeErr) {
		t.Errorf("FactorLookupError = %+v, want UserID 7 wrapping the store error", lookupErr)
	}
	if view.MFAEnabled || view.Factors != nil {
		t.Errorf("ResolveFactorView(List error) view = %+v, want zero", view)
	}
}

func TestResolveFactorView_ListsOnce(t *testing.T) {
	lister := &countingFactorLister{fakeFactorLister: fakeFactorLister{factors: []*auth.AuthFactor{
		activeFactor("f1", "fido2", time.Now()),
	}}}
	if _, err := ResolveFactorView(context.Background(), lister, 1); err != nil {
		t.Fatalf("ResolveFactorView() error = %v", err)
	}
	if lister.calls != 1 {
		t.Errorf("List called %d times, want exactly 1", lister.calls)
	}
}

func TestResolveFactorView_MFAEnabled(t *testing.T) {
	tests := []struct {
		name   string
		lister FactorLister
		want   bool
	}{
		{"nil lister", nil, false},
		{"no factors", &fakeFactorLister{}, false},
		{"one active factor", &fakeFactorLister{factors: []*auth.AuthFactor{
			activeFactor("f1", "fido2", time.Now()),
		}}, true},
		{"only revoked factors", &fakeFactorLister{factors: []*auth.AuthFactor{
			revokedFactor("f1", "fido2", time.Now()),
		}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view, err := ResolveFactorView(context.Background(), tt.lister, 1)
			if err != nil {
				t.Fatalf("ResolveFactorView() error = %v", err)
			}
			if view.MFAEnabled != tt.want {
				t.Errorf("MFAEnabled = %v, want %v", view.MFAEnabled, tt.want)
			}
		})
	}
}

func TestResolveFactorView_Methods(t *testing.T) {
	now := time.Now().UTC()
	lister := &fakeFactorLister{factors: []*auth.AuthFactor{
		activeFactor("f1", "fido2", now),
		activeFactor("f2", "piv", now),
		revokedFactor("f3", "tpm2", now),
		activeFactor("f4", "fido2", now), // duplicate type, deduplicated
	}}
	view, err := ResolveFactorView(context.Background(), lister, 1)
	if err != nil {
		t.Fatalf("ResolveFactorView() error = %v", err)
	}
	want := []string{"fido2", "piv"}
	if len(view.MFAMethods) != len(want) {
		t.Fatalf("MFAMethods = %v, want %v", view.MFAMethods, want)
	}
	for i := range want {
		if view.MFAMethods[i] != want[i] {
			t.Errorf("MFAMethods[%d] = %q, want %q", i, view.MFAMethods[i], want[i])
		}
	}
}

func TestResolveFactorView_EnrolledAt(t *testing.T) {
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		factors []*auth.AuthFactor
		want    string
	}{
		{"earliest wins regardless of order", []*auth.AuthFactor{
			activeFactor("f1", "piv", late),
			activeFactor("f2", "fido2", early),
		}, early.Format(time.RFC3339)},
		{"revoked earlier factor ignored", []*auth.AuthFactor{
			revokedFactor("f1", "piv", early),
			activeFactor("f2", "fido2", late),
		}, late.Format(time.RFC3339)},
		{"no factors", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view, err := ResolveFactorView(context.Background(), &fakeFactorLister{factors: tt.factors}, 1)
			if err != nil {
				t.Fatalf("ResolveFactorView() error = %v", err)
			}
			if view.MFAEnrolledAt != tt.want {
				t.Errorf("MFAEnrolledAt = %q, want %q", view.MFAEnrolledAt, tt.want)
			}
		})
	}
}

func TestResolveFactorView_FactorsAreMetadataOnly(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	lister := &fakeFactorLister{factors: []*auth.AuthFactor{
		{ID: "f1", Type: "fido2", RegisteredAt: now, Credential: []byte("do-not-leak")},
		revokedFactor("f2", "piv", now),
	}}
	view, err := ResolveFactorView(context.Background(), lister, 1)
	if err != nil {
		t.Fatalf("ResolveFactorView() error = %v", err)
	}
	if len(view.Factors) != 1 {
		t.Fatalf("Factors len = %d, want 1 (revoked excluded)", len(view.Factors))
	}
	got := view.Factors[0]
	if got.ID != "f1" || got.FactorName != "fido2" || got.CreatedAt != now.Format(time.RFC3339) {
		t.Errorf("Factors[0] = %+v, want ID=f1 FactorName=fido2 CreatedAt=%s", got, now.Format(time.RFC3339))
	}
}

func TestFactorLookupError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *FactorLookupError
		want string
	}{
		{"with cause", &FactorLookupError{UserID: 3, Err: errors.New("boom")}, "user: could not list auth factors for user 3: boom"},
		{"without cause", &FactorLookupError{UserID: 3}, "user: could not list auth factors for user 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

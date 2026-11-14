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

package firstboot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// soCreateOnly is a user surface with no UserResolver: the SO's create is
// answered by create, every other account by the harness's users.
type soCreateOnly struct {
	users  *soUsers
	soName string
	create func() (*user.User, error)
}

func (u *soCreateOnly) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	if req.Username == u.soName {
		return u.create()
	}
	return u.users.CreateUser(ctx, req)
}

// soLookupMiss adds a resolver that never finds the SO.
type soLookupMiss struct {
	*soCreateOnly
}

func (u *soLookupMiss) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	if username == u.soName {
		return nil, errors.New("not found")
	}
	return u.users.GetUserByUsername(ctx, tenantID, username)
}

// soBindFails refuses the SO's binding and passes every other through.
type soBindFails struct {
	*soBinder
	soName string
}

func (b *soBindFails) BindAdminCert(ctx context.Context, tenantID string, userID uint64, thumbprint string) error {
	if so := b.users.byName[b.soName]; so != nil && so.ID == userID {
		return errors.New("bind refused")
	}
	return b.soBinder.BindAdminCert(ctx, tenantID, userID, thumbprint)
}

func TestInitialize_SystemSOAccountFailures(t *testing.T) {
	shrinkAdminConvergeWindow(t, 30*time.Millisecond, time.Millisecond)
	taken := func() (*user.User, error) { return nil, &user.UsernameExistsError{Username: "officer"} }
	idTaken := func() (*user.User, error) { return nil, &user.UserIDExistsError{ID: 7} }
	tests := []struct {
		name     string
		create   func() (*user.User, error)
		resolver bool
	}{
		{"create fails", func() (*user.User, error) { return nil, errors.New("store down") }, true},
		{"create returns no user", func() (*user.User, error) { return nil, nil }, true},
		{"username taken, no resolver", taken, false},
		{"username taken, never converges", taken, true},
		{"id taken on every attempt", idTaken, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newSOHarness(t, true)
			c := &soCreateOnly{users: h.users, soName: h.deps.SO.Username, create: tc.create}
			h.deps.Users = c
			if tc.resolver {
				h.deps.Users = &soLookupMiss{c}
			}
			_, err := Initialize(context.Background(), h.deps)
			var e *SystemSOEnrollmentError
			if !errors.As(err, &e) || e.Op != "account" {
				t.Fatalf("Initialize = %v, want an account failure", err)
			}
		})
	}
}

func TestInitialize_SystemSOConvergeHonoursContext(t *testing.T) {
	h := newSOHarness(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	c := &soCreateOnly{users: h.users, soName: h.deps.SO.Username, create: func() (*user.User, error) {
		calls++
		cancel()
		return nil, &user.UsernameExistsError{Username: "officer"}
	}}
	h.deps.Users = &soLookupMiss{c}
	_, err := Initialize(ctx, h.deps)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Initialize = %v, want the context's cancellation", err)
	}
	if calls != 1 {
		t.Fatalf("SO create calls = %d, want 1", calls)
	}
}

func TestInitialize_SystemSOBindFailure(t *testing.T) {
	h := newSOHarness(t, true)
	h.deps.Binder = &soBindFails{soBinder: h.binder, soName: h.deps.SO.Username}
	_, err := Initialize(context.Background(), h.deps)
	var e *SystemSOEnrollmentError
	if !errors.As(err, &e) || e.Op != "binding" {
		t.Fatalf("Initialize = %v, want a binding failure", err)
	}
}

func TestSOConfig_ValidateNamesTheMissingPart(t *testing.T) {
	for name, tc := range map[string]struct {
		so    SOConfig
		field string
	}{
		"no username":    {SOConfig{CSRPEM: []byte("x"), PINRecord: "r"}, "so_username"},
		"no certificate": {SOConfig{Username: "officer", PINRecord: "r"}, "so_csr_pem or so_cert_pem"},
	} {
		t.Run(name, func(t *testing.T) {
			var e *IncompleteSOError
			if err := tc.so.Validate(); !errors.As(err, &e) || e.Field != tc.field {
				t.Fatalf("Validate = %v, want the missing %s", err, tc.field)
			}
		})
	}
}

// pinOnly sets a PIN and cannot commit a hashed PIN record.
type pinOnly struct{}

func (pinOnly) SetSOPIN(context.Context, string, []byte) error { return nil }

func TestCommitSystemSOPIN_RecordNeedsARecordSeam(t *testing.T) {
	err := commitSystemSOPIN(context.Background(), pinOnly{}, "system", &SOConfig{Username: "officer", PINRecord: "r"})
	var e *MissingSOSeamError
	if !errors.As(err, &e) {
		t.Fatalf("commitSystemSOPIN = %v, want MissingSOSeamError", err)
	}
}

func TestInitialize_SystemSOAdoptsPresentedCertWithChain(t *testing.T) {
	h := newSOHarness(t, true)
	certPEM, pool := issueNamedCertFromCA(t, h.deps.SO.Username, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	h.deps.SO.CSRPEM = nil
	h.deps.SO.CertPEM = certPEM
	// No intermediate is needed here; the chain is read and must not stop
	// the leaf verifying against its root.
	h.deps.SO.ChainPEM = certPEM
	h.deps.AdminCertPool = pool
	result, err := Initialize(context.Background(), h.deps)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if want, _ := spkiThumbprintFromCertPEM(certPEM); result.SOCertThumbprint != want {
		t.Fatalf("bound %q, want the presented certificate %q", result.SOCertThumbprint, want)
	}
}

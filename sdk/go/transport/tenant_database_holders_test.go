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

package transport

import (
	"context"
	"errors"
	"testing"
)

// The database share-holder methods of the test doubles. Like every other
// per-database call of mockDatabaseClient, each refuses a database it does
// not hold with DatabaseNotFoundError; a held database records the call.

var (
	_ DatabaseClient = (*mockDatabaseClient)(nil)
	_ DatabaseClient = (*failingDatabaseClient)(nil)
)

// holderCall records that a share-holder method reached the inner client.
func (m *mockDatabaseClient) holderCall(method, name string) error {
	if _, ok := m.databases[name]; !ok {
		return &DatabaseNotFoundError{Name: name}
	}
	m.holderCalls = append(m.holderCalls, method+":"+name)
	return nil
}

func (m *mockDatabaseClient) DatabaseBarrierOfficerRegister(_ context.Context, req *DatabaseOfficerRegisterRequest) (*OfficerRegisterResponse, error) {
	if err := m.holderCall("OfficerRegister", req.Database); err != nil {
		return nil, err
	}
	return &OfficerRegisterResponse{}, nil
}

func (m *mockDatabaseClient) DatabaseBarrierOfficerList(_ context.Context, name string) ([]*OfficerStatus, error) {
	if err := m.holderCall("OfficerList", name); err != nil {
		return nil, err
	}
	return []*OfficerStatus{}, nil
}

func (m *mockDatabaseClient) DatabaseBarrierShamirInitialize(_ context.Context, req *DatabaseShamirInitializeRequest) (*BarrierInitializeShamirResponse, error) {
	if err := m.holderCall("ShamirInitialize", req.Database); err != nil {
		return nil, err
	}
	return &BarrierInitializeShamirResponse{}, nil
}

func (m *mockDatabaseClient) DatabaseBarrierShareClaimBegin(_ context.Context, req *DatabaseShareClaimBeginRequest) (*ShareClaimBeginResponse, error) {
	if err := m.holderCall("ShareClaimBegin", req.Database); err != nil {
		return nil, err
	}
	return &ShareClaimBeginResponse{}, nil
}

func (m *mockDatabaseClient) DatabaseBarrierShareClaimComplete(_ context.Context, req *DatabaseShareClaimCompleteRequest) (*ShareClaimCompleteResponse, error) {
	if err := m.holderCall("ShareClaimComplete", req.Database); err != nil {
		return nil, err
	}
	return &ShareClaimCompleteResponse{}, nil
}

func (m *mockDatabaseClient) DatabaseBarrierQuorumBegin(_ context.Context, req *DatabaseQuorumBeginRequest) (*QuorumChallenge, error) {
	if err := m.holderCall("QuorumBegin", req.Database); err != nil {
		return nil, err
	}
	return &QuorumChallenge{}, nil
}

func (m *mockDatabaseClient) DatabaseBarrierQuorumSubmit(_ context.Context, req *DatabaseQuorumSubmitRequest) (*QuorumOutcome, error) {
	if err := m.holderCall("QuorumSubmit", req.Database); err != nil {
		return nil, err
	}
	return &QuorumOutcome{}, nil
}

func (m *mockDatabaseClient) DatabaseBarrierRekeyStart(_ context.Context, req *DatabaseRekeyStartRequest) (*QuorumRound, error) {
	if err := m.holderCall("RekeyStart", req.Database); err != nil {
		return nil, err
	}
	return &QuorumRound{}, nil
}

func (m *mockDatabaseClient) DatabaseBarrierOfficerRevoke(_ context.Context, req *DatabaseOfficerRevokeRequest) (*QuorumRound, error) {
	if err := m.holderCall("OfficerRevoke", req.Database); err != nil {
		return nil, err
	}
	return &QuorumRound{}, nil
}

func (m *mockDatabaseClient) DatabaseBarrierOfficerUnenrol(_ context.Context, req *DatabaseOfficerUnenrolRequest) error {
	return m.holderCall("OfficerUnenrol", req.Database)
}

func (f *failingDatabaseClient) DatabaseBarrierOfficerRegister(context.Context, *DatabaseOfficerRegisterRequest) (*OfficerRegisterResponse, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) DatabaseBarrierOfficerList(context.Context, string) ([]*OfficerStatus, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) DatabaseBarrierShamirInitialize(context.Context, *DatabaseShamirInitializeRequest) (*BarrierInitializeShamirResponse, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) DatabaseBarrierShareClaimBegin(context.Context, *DatabaseShareClaimBeginRequest) (*ShareClaimBeginResponse, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) DatabaseBarrierShareClaimComplete(context.Context, *DatabaseShareClaimCompleteRequest) (*ShareClaimCompleteResponse, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) DatabaseBarrierQuorumBegin(context.Context, *DatabaseQuorumBeginRequest) (*QuorumChallenge, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) DatabaseBarrierQuorumSubmit(context.Context, *DatabaseQuorumSubmitRequest) (*QuorumOutcome, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) DatabaseBarrierRekeyStart(context.Context, *DatabaseRekeyStartRequest) (*QuorumRound, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) DatabaseBarrierOfficerRevoke(context.Context, *DatabaseOfficerRevokeRequest) (*QuorumRound, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) DatabaseBarrierOfficerUnenrol(context.Context, *DatabaseOfficerUnenrolRequest) error {
	return f.err
}

// holderOps calls every share-holder method of c on database name and
// returns each call's error by method.
func holderOps(ctx context.Context, c DatabaseClient, name string) map[string]error {
	errs := make(map[string]error)
	_, errs["OfficerRegister"] = c.DatabaseBarrierOfficerRegister(ctx, &DatabaseOfficerRegisterRequest{Database: name})
	_, errs["OfficerList"] = c.DatabaseBarrierOfficerList(ctx, name)
	_, errs["ShamirInitialize"] = c.DatabaseBarrierShamirInitialize(ctx, &DatabaseShamirInitializeRequest{Database: name})
	_, errs["ShareClaimBegin"] = c.DatabaseBarrierShareClaimBegin(ctx, &DatabaseShareClaimBeginRequest{Database: name})
	_, errs["ShareClaimComplete"] = c.DatabaseBarrierShareClaimComplete(ctx, &DatabaseShareClaimCompleteRequest{Database: name})
	_, errs["QuorumBegin"] = c.DatabaseBarrierQuorumBegin(ctx, &DatabaseQuorumBeginRequest{Database: name})
	_, errs["QuorumSubmit"] = c.DatabaseBarrierQuorumSubmit(ctx, &DatabaseQuorumSubmitRequest{Database: name})
	_, errs["RekeyStart"] = c.DatabaseBarrierRekeyStart(ctx, &DatabaseRekeyStartRequest{Database: name})
	_, errs["OfficerRevoke"] = c.DatabaseBarrierOfficerRevoke(ctx, &DatabaseOfficerRevokeRequest{Database: name})
	errs["OfficerUnenrol"] = c.DatabaseBarrierOfficerUnenrol(ctx, &DatabaseOfficerUnenrolRequest{Database: name})
	return errs
}

// TestTenantDatabaseClient_HolderOps_ForwardOwned verifies every holder
// operation on a database the tenant owns reaches the inner client.
func TestTenantDatabaseClient_HolderOps_ForwardOwned(t *testing.T) {
	ctx := context.Background()
	mock := newMockDatabaseClient()
	mock.databases["vault"] = &DatabaseInfo{TenantID: "acme", Name: "vault"}
	tc, err := WithTenantDatabase(mock, "acme")
	if err != nil {
		t.Fatalf("WithTenantDatabase: %v", err)
	}
	for method, err := range holderOps(ctx, tc, "vault") {
		if err != nil {
			t.Errorf("%s on an owned database: %v", method, err)
		}
	}
	if len(mock.holderCalls) != 10 {
		t.Errorf("inner client saw %d holder calls, want 10: %v", len(mock.holderCalls), mock.holderCalls)
	}
}

// TestTenantDatabaseClient_HolderOps_RefuseForeign verifies a database owned
// by another tenant is refused before the call is forwarded.
func TestTenantDatabaseClient_HolderOps_RefuseForeign(t *testing.T) {
	ctx := context.Background()
	mock := newMockDatabaseClient()
	mock.databases["vault"] = &DatabaseInfo{TenantID: "other", Name: "vault"}
	tc, err := WithTenantDatabase(mock, "acme")
	if err != nil {
		t.Fatalf("WithTenantDatabase: %v", err)
	}
	for method, err := range holderOps(ctx, tc, "vault") {
		var denied *TenantDatabaseAccessDeniedError
		if !errors.As(err, &denied) {
			t.Errorf("%s on a foreign database: got %v, want TenantDatabaseAccessDeniedError", method, err)
		}
	}
	if len(mock.holderCalls) != 0 {
		t.Errorf("a foreign database's holder calls were forwarded: %v", mock.holderCalls)
	}
}

// TestTenantDatabaseClient_HolderOps_RefuseNilRequest verifies a nil request
// names no database and is refused rather than dereferenced.
func TestTenantDatabaseClient_HolderOps_RefuseNilRequest(t *testing.T) {
	ctx := context.Background()
	tc, err := WithTenantDatabase(newMockDatabaseClient(), "acme")
	if err != nil {
		t.Fatalf("WithTenantDatabase: %v", err)
	}
	errs := []error{}
	_, e := tc.DatabaseBarrierOfficerRegister(ctx, nil)
	errs = append(errs, e)
	_, e = tc.DatabaseBarrierShamirInitialize(ctx, nil)
	errs = append(errs, e)
	_, e = tc.DatabaseBarrierShareClaimBegin(ctx, nil)
	errs = append(errs, e)
	_, e = tc.DatabaseBarrierShareClaimComplete(ctx, nil)
	errs = append(errs, e)
	_, e = tc.DatabaseBarrierQuorumBegin(ctx, nil)
	errs = append(errs, e)
	_, e = tc.DatabaseBarrierQuorumSubmit(ctx, nil)
	errs = append(errs, e)
	_, e = tc.DatabaseBarrierRekeyStart(ctx, nil)
	errs = append(errs, e)
	_, e = tc.DatabaseBarrierOfficerRevoke(ctx, nil)
	errs = append(errs, e)
	errs = append(errs, tc.DatabaseBarrierOfficerUnenrol(ctx, nil))
	for i, err := range errs {
		var required *DatabaseNameRequiredError
		if !errors.As(err, &required) {
			t.Errorf("call %d: got %v, want DatabaseNameRequiredError", i, err)
		}
	}
}

// TestTenantDatabaseClient_HolderOps_InnerErrorPropagates verifies an
// ownership lookup failure is returned unchanged.
func TestTenantDatabaseClient_HolderOps_InnerErrorPropagates(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("backend down")
	tc, err := WithTenantDatabase(&failingDatabaseClient{err: sentinel}, "acme")
	if err != nil {
		t.Fatalf("WithTenantDatabase: %v", err)
	}
	for method, err := range holderOps(ctx, tc, "vault") {
		if !errors.Is(err, sentinel) {
			t.Errorf("%s: got %v, want the lookup failure", method, err)
		}
	}
}

// TestDatabaseNameRequiredError_Message verifies the error names its method.
func TestDatabaseNameRequiredError_Message(t *testing.T) {
	err := &DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerList"}
	if got, want := err.Error(), "transport: DatabaseBarrierOfficerList requires a database name"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

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

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// custodianCeremonyServer is an in-memory HTTP stub for the factor-verified
// custodian ceremony + non-versioned credential store/retrieve routes.
type custodianCeremonyServer struct {
	storeName  string
	storeValue string
	failStore  bool

	retrieveValue string
	failRetrieve  bool
}

func (s *custodianCeremonyServer) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/credentials/store", func(w http.ResponseWriter, r *http.Request) {
		if s.failStore {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		var body struct{ Name, Value string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.storeName, s.storeValue = body.Name, body.Value
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/v1/credentials/db-cred/value", func(w http.ResponseWriter, _ *http.Request) {
		if s.failRetrieve {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"value": s.retrieveValue})
	})

	mux.HandleFunc("/v1/ceremony/claim-share/begin", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(&transport.CeremonyClaimShareBeginResponse{
			CustodianID: "officer-1",
			Nonce:       []byte{0x01, 0x02, 0x03},
			ExpiresAt:   time.Unix(1900000000, 0).UTC(),
		})
	})

	mux.HandleFunc("/v1/ceremony/claim-share/complete", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(&transport.CeremonyCustodianInfo{
			ID: "officer-1", Username: "alice", ShareIndex: 2, ShareClaimed: true,
		})
	})

	mux.HandleFunc("/v1/barrier/unseal-share", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(&transport.CeremonySubmitUnsealShareResponse{
			Submitted: 3, Required: 3, Unsealed: true,
		})
	})

	return mux
}

func newCustodianCeremonyTestTransport(t *testing.T) (*Transport, *custodianCeremonyServer) {
	t.Helper()
	srv := &custodianCeremonyServer{}
	httpSrv := httptest.NewServer(srv.handler())
	t.Cleanup(httpSrv.Close)
	return newWiredTransport(httpSrv), srv
}

func TestRESTCredentialStore_RoundTrip(t *testing.T) {
	rt, srv := newCustodianCeremonyTestTransport(t)
	if err := rt.CredentialStore(context.Background(), "db-cred", []byte("p@ss")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}
	if srv.storeName != "db-cred" || srv.storeValue != "p@ss" {
		t.Errorf("server got name=%q value=%q", srv.storeName, srv.storeValue)
	}
}

func TestRESTCredentialStore_ServerError(t *testing.T) {
	rt, srv := newCustodianCeremonyTestTransport(t)
	srv.failStore = true
	if err := rt.CredentialStore(context.Background(), "db-cred", []byte("x")); err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTCredentialStore_AfterClose(t *testing.T) {
	rt, _ := newCustodianCeremonyTestTransport(t)
	_ = rt.Close()
	err := rt.CredentialStore(context.Background(), "db-cred", []byte("x"))
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %v", err)
	}
}

func TestRESTCredentialRetrieve_RoundTrip(t *testing.T) {
	rt, srv := newCustodianCeremonyTestTransport(t)
	srv.retrieveValue = "p@ss"
	val, err := rt.CredentialRetrieve(context.Background(), "db-cred")
	if err != nil {
		t.Fatalf("CredentialRetrieve: %v", err)
	}
	if string(val) != "p@ss" {
		t.Errorf("val = %q", val)
	}
}

func TestRESTCredentialRetrieve_NotFound(t *testing.T) {
	rt, srv := newCustodianCeremonyTestTransport(t)
	srv.failRetrieve = true
	if _, err := rt.CredentialRetrieve(context.Background(), "db-cred"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTCeremonyClaimShareBegin_RoundTrip(t *testing.T) {
	rt, _ := newCustodianCeremonyTestTransport(t)
	resp, err := rt.CeremonyClaimShareBegin(context.Background(), &transport.CeremonyClaimShareBeginRequest{CustodianID: "officer-1"})
	if err != nil {
		t.Fatalf("CeremonyClaimShareBegin: %v", err)
	}
	if resp.CustodianID != "officer-1" || len(resp.Nonce) != 3 || resp.ExpiresAt.Unix() != 1900000000 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestRESTCeremonyClaimShareComplete_RoundTrip(t *testing.T) {
	rt, _ := newCustodianCeremonyTestTransport(t)
	info, err := rt.CeremonyClaimShareComplete(context.Background(), &transport.CeremonyClaimShareCompleteRequest{
		CustodianID: "officer-1", ChallengeID: "chal-1", FactorResponse: []byte{0x09},
	})
	if err != nil {
		t.Fatalf("CeremonyClaimShareComplete: %v", err)
	}
	if info.ID != "officer-1" || info.Username != "alice" || info.ShareIndex != 2 || !info.ShareClaimed {
		t.Fatalf("info = %+v", info)
	}
}

func TestRESTCeremonySubmitUnsealShare_RoundTrip(t *testing.T) {
	rt, _ := newCustodianCeremonyTestTransport(t)
	resp, err := rt.CeremonySubmitUnsealShare(context.Background(), &transport.CeremonySubmitUnsealShareRequest{
		CustodianID: "officer-1", ChallengeID: "chal-1", ShareBytes: []byte{0x0a},
	})
	if err != nil {
		t.Fatalf("CeremonySubmitUnsealShare: %v", err)
	}
	if resp.Submitted != 3 || resp.Required != 3 || !resp.Unsealed {
		t.Fatalf("resp = %+v", resp)
	}
}

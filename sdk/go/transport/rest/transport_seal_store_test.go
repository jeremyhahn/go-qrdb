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

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestRESTSealStore_Calls(t *testing.T) {
	// A seal-store secret is sent and returned only over TLS (#1647).
	rs := &routerServer{}
	srv := httptest.NewTLSServer(rs)
	t.Cleanup(srv.Close)
	rt := newWiredTransport(srv)
	ctx := context.Background()
	var put sealStoreSecretWire
	var deleted, resealed bool

	rs.handle(exactPath(http.MethodPost, "/v1/seal-store/secrets/a b"), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&put)
		w.WriteHeader(http.StatusNoContent)
	})
	rs.handle(exactPath(http.MethodGet, "/v1/seal-store/secrets/alpha"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, sealStoreSecretWire{Name: "alpha", Secret: []byte("s3cret")})
	})
	rs.handle(exactPath(http.MethodDelete, "/v1/seal-store/secrets/alpha"), func(w http.ResponseWriter, _ *http.Request) {
		deleted = true
		w.WriteHeader(http.StatusNoContent)
	})
	rs.handle(exactPath(http.MethodGet, "/v1/seal-store/secrets"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"names": []string{"alpha", "beta"}})
	})
	rs.handle(exactPath(http.MethodPost, "/v1/seal-store/secrets/alpha/reseal"), func(w http.ResponseWriter, _ *http.Request) {
		resealed = true
		w.WriteHeader(http.StatusNoContent)
	})
	rs.handle(exactPath(http.MethodGet, "/v1/seal-store/status"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, transport.SealStoreStatusResponse{Available: true, SealerID: "passphrase", SecretCount: 2, SecretNames: []string{"alpha", "beta"}})
	})

	if err := rt.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "a b", Secret: []byte("x")}); err != nil {
		t.Fatalf("SealStorePut = %v", err)
	}
	if string(put.Secret) != "x" {
		t.Fatalf("put body = %+v, want secret x", put)
	}
	got, err := rt.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "alpha"})
	if err != nil || string(got.Secret) != "s3cret" {
		t.Fatalf("SealStoreGet = %+v, %v", got, err)
	}
	if err := rt.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "alpha"}); err != nil || !deleted {
		t.Fatalf("SealStoreDelete = %v, reached=%v", err, deleted)
	}
	list, err := rt.SealStoreList(ctx)
	if err != nil || !slices.Equal(list.Names, []string{"alpha", "beta"}) {
		t.Fatalf("SealStoreList = %+v, %v", list, err)
	}
	if err := rt.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "alpha"}); err != nil || !resealed {
		t.Fatalf("SealStoreReseal = %v, reached=%v", err, resealed)
	}
	st, err := rt.SealStoreStatus(ctx)
	if err != nil || !st.Available || st.SealerID != "passphrase" || st.SecretCount != 2 || !slices.Equal(st.SecretNames, []string{"alpha", "beta"}) {
		t.Fatalf("SealStoreStatus = %+v, %v", st, err)
	}
}

// sealStoreSecretCalls are the two seal-store calls that carry a secret:
// put sends one, get returns one.
var sealStoreSecretCalls = []struct {
	name string
	call func(*Transport) error
}{
	{"put", func(tr *Transport) error {
		return tr.SealStorePut(context.Background(), &transport.SealStorePutRequest{Name: "alpha", Secret: []byte("s3cret")})
	}},
	{"get", func(tr *Transport) error {
		_, err := tr.SealStoreGet(context.Background(), &transport.SealStoreGetRequest{Name: "alpha"})
		return err
	}},
}

// TestRESTSealStore_SecretRequiresTLS proves a seal-store put or get is
// refused over http:// before any request reaches the server.
func TestRESTSealStore_SecretRequiresTLS(t *testing.T) {
	for _, c := range sealStoreSecretCalls {
		t.Run(c.name, func(t *testing.T) {
			var reached atomic.Int64
			plain := httptest.NewServer(secretCounter(&reached))
			t.Cleanup(plain.Close)
			tr := connectREST(t, plain.URL, nil)

			err := c.call(tr)
			var tlsErr *transport.BarrierSecretRequiresTLSError
			if !errors.As(err, &tlsErr) || tlsErr.Kind != transport.BarrierSecretSealStore {
				t.Fatalf("err = %v, want *transport.BarrierSecretRequiresTLSError for the seal-store secret", err)
			}
			if n := reached.Load(); n != 0 {
				t.Fatalf("the server saw %d seal-store requests over http://, want none", n)
			}
		})
	}
}

// TestRESTSealStore_PlaintextRedirectIsRefused proves a seal-store put or get
// never follows a redirect from https:// to http://.
func TestRESTSealStore_PlaintextRedirectIsRefused(t *testing.T) {
	for _, c := range sealStoreSecretCalls {
		t.Run(c.name, func(t *testing.T) {
			var plainReached atomic.Int64
			plain := httptest.NewServer(secretCounter(&plainReached))
			t.Cleanup(plain.Close)
			secure := httptest.NewTLSServer(redirectTo(plain.URL, http.StatusTemporaryRedirect))
			t.Cleanup(secure.Close)
			tr := connectREST(t, secure.URL, trusting(secure))

			err := c.call(tr)
			var tlsErr *transport.BarrierSecretRequiresTLSError
			if !errors.As(err, &tlsErr) || tlsErr.Kind != transport.BarrierSecretSealStore {
				t.Fatalf("err = %v, want *transport.BarrierSecretRequiresTLSError for the seal-store secret", err)
			}
			if n := plainReached.Load(); n != 0 {
				t.Fatalf("the seal-store call followed a redirect to http:// (%d requests)", n)
			}
		})
	}
}

// TestRESTSealStore_ForeignHostRedirectIsRefused proves a seal-store put or
// get never follows a 307 or 308 to another host.
func TestRESTSealStore_ForeignHostRedirectIsRefused(t *testing.T) {
	for _, c := range sealStoreSecretCalls {
		for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			t.Run(c.name+"/"+http.StatusText(status), func(t *testing.T) {
				foreign := &bodyRecorder{}
				foreignSrv := httptest.NewTLSServer(foreign)
				t.Cleanup(foreignSrv.Close)
				origin := httptest.NewTLSServer(redirectTo(foreignSrv.URL, status))
				t.Cleanup(origin.Close)
				tr := connectREST(t, origin.URL, trusting(origin))

				err := c.call(tr)
				var hostErr *transport.BarrierSecretForeignHostError
				if !errors.As(err, &hostErr) || hostErr.Kind != transport.BarrierSecretSealStore {
					t.Fatalf("err = %v, want *transport.BarrierSecretForeignHostError for the seal-store secret", err)
				}
				if n, body := foreign.seen(); n != 0 || body != 0 {
					t.Fatalf("the foreign host saw %d requests and %d body bytes, want none", n, body)
				}
			})
		}
	}
}

func TestRESTSealStore_ForbiddenIsAnError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/seal-store/status"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "permission denied", http.StatusForbidden)
	})
	_, err := rt.SealStoreStatus(context.Background())
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != http.StatusForbidden {
		t.Fatalf("SealStoreStatus = %v, want a 403 RequestError", err)
	}
}

// sealStoreRefusal answers as the REST server's writeErrorJSON does for a
// seal-store error: the status, the message and the structured refusal
// reason errclass.Refusal attaches.
func sealStoreRefusal(t *testing.T, status int, sentinel error) http.HandlerFunc {
	t.Helper()
	reason, ok := transport.BarrierRefusal(sentinel)
	if !ok {
		t.Fatalf("BarrierRefusal(%v) has no reason", sentinel)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{Error: sentinel.Error(), Code: reason})
	}
}

// TestRESTSealStore_MissingSecretIsSealStoreNotFound proves a missing
// secret, an invalid name and a sealed seal-store barrier reach a REST caller
// as the seal sentinel the embedded transport returns (#1647).
func TestRESTSealStore_MissingSecretIsSealStoreNotFound(t *testing.T) {
	rs := &routerServer{}
	srv := httptest.NewTLSServer(rs)
	t.Cleanup(srv.Close)
	rt := newWiredTransport(srv)
	rs.handle(exactPath(http.MethodGet, "/v1/seal-store/secrets/absent"), sealStoreRefusal(t, http.StatusNotFound, seal.ErrSealStoreSecretNotFound))
	rs.handle(exactPath(http.MethodDelete, "/v1/seal-store/secrets/absent"), sealStoreRefusal(t, http.StatusNotFound, seal.ErrSealStoreSecretNotFound))
	rs.handle(exactPath(http.MethodGet, "/v1/seal-store/secrets/bad"), sealStoreRefusal(t, http.StatusBadRequest, seal.ErrInvalidSecretName))
	rs.handle(exactPath(http.MethodGet, "/v1/seal-store/secrets/sealed"), sealStoreRefusal(t, http.StatusServiceUnavailable, seal.ErrSealStoreBarrierSealed))
	ctx := context.Background()
	var nf *transport.KeyNotFoundError
	_, err := rt.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "absent"})
	if !errors.Is(err, seal.ErrSealStoreSecretNotFound) || errors.As(err, &nf) {
		t.Fatalf("SealStoreGet of a missing secret = %v, want errors.Is seal.ErrSealStoreSecretNotFound, not KeyNotFoundError", err)
	}
	err = rt.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "absent"})
	if !errors.Is(err, seal.ErrSealStoreSecretNotFound) || errors.As(err, &nf) {
		t.Fatalf("SealStoreDelete of a missing secret = %v, want errors.Is seal.ErrSealStoreSecretNotFound, not KeyNotFoundError", err)
	}
	if _, err := rt.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "bad"}); !errors.Is(err, seal.ErrInvalidSecretName) {
		t.Fatalf("SealStoreGet of an invalid name = %v, want errors.Is seal.ErrInvalidSecretName", err)
	}
	if _, err := rt.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "sealed"}); !errors.Is(err, seal.ErrSealStoreBarrierSealed) {
		t.Fatalf("SealStoreGet on a sealed seal-store barrier = %v, want errors.Is seal.ErrSealStoreBarrierSealed", err)
	}
}

func TestRESTSealStore_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	var ce *transport.ClosedError
	if _, err := rt.SealStoreStatus(context.Background()); !errors.As(err, &ce) {
		t.Fatalf("SealStoreStatus = %T, want ClosedError", err)
	}
	if err := rt.SealStorePut(context.Background(), &transport.SealStorePutRequest{Name: "a", Secret: []byte("x")}); !errors.As(err, &ce) {
		t.Fatalf("SealStorePut = %T, want ClosedError", err)
	}
}

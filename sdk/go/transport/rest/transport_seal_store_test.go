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
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestRESTSealStore_Calls(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
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

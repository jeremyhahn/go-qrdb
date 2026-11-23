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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func respondEmpty([]byte) (uint16, []byte) { return wire.StatusOK, nil }

func TestE2E_SealStore(t *testing.T) {
	srv, tr := startE2EServer(t)
	ctx := context.Background()
	secret := []byte{0x00, 0xff, 0x10}

	srv.SetResponder("sealstore.Put", respondEmpty)
	if err := tr.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "jwt", Secret: secret}); err != nil {
		t.Fatalf("SealStorePut = %v", err)
	}
	rec := srv.LastRequest("sealstore.Put")
	if rec == nil {
		t.Fatal("no recorded sealstore.Put request")
	}
	var put sealStorePutWire
	if err := json.Unmarshal(rec.Payload, &put); err != nil || put.Name != "jwt" || !slices.Equal(put.Secret, secret) {
		t.Fatalf("Put wire body = %+v, %v", put, err)
	}

	srv.SetResponder("sealstore.Get", respondJSON(transport.SealStoreGetResponse{Name: "jwt", Secret: secret}))
	got, err := tr.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "jwt"})
	if err != nil || got.Name != "jwt" || !slices.Equal(got.Secret, secret) {
		t.Fatalf("SealStoreGet = %+v, %v", got, err)
	}

	srv.SetResponder("sealstore.List", respondJSON(map[string]any{"names": []string{"jwt", "tls"}}))
	list, err := tr.SealStoreList(ctx)
	if err != nil || !slices.Equal(list.Names, []string{"jwt", "tls"}) {
		t.Fatalf("SealStoreList = %+v, %v", list, err)
	}

	srv.SetResponder("sealstore.Reseal", respondEmpty)
	if err := tr.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "tls"}); err != nil {
		t.Fatalf("SealStoreReseal = %v", err)
	}
	var name sealStoreNameWire
	if rec := srv.LastRequest("sealstore.Reseal"); rec == nil || json.Unmarshal(rec.Payload, &name) != nil || name.Name != "tls" {
		t.Fatalf("Reseal wire body = %+v", name)
	}

	want := transport.SealStoreStatusResponse{Available: true, SealerID: "pkcs11", SecretCount: 2, SecretNames: []string{"jwt", "tls"}}
	srv.SetResponder("sealstore.Status", respondJSON(want))
	st, err := tr.SealStoreStatus(ctx)
	if err != nil || st.Available != want.Available || st.SealerID != want.SealerID ||
		st.SecretCount != want.SecretCount || !slices.Equal(st.SecretNames, want.SecretNames) {
		t.Fatalf("SealStoreStatus = %+v, %v", st, err)
	}

	srv.SetResponder("sealstore.Delete", respondEmpty)
	if err := tr.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "jwt"}); err != nil {
		t.Fatalf("SealStoreDelete = %v", err)
	}
	if srv.LastRequest("sealstore.Delete") == nil {
		t.Fatal("no recorded sealstore.Delete request")
	}
}

func TestE2E_SealStorePermissionDenied(t *testing.T) {
	srv, tr := startE2EServer(t)
	for _, method := range []string{"Put", "Get", "Delete", "List", "Reseal", "Status"} {
		srv.SetResponder("sealstore."+method, func([]byte) (uint16, []byte) {
			return wire.StatusPermissionDenied, []byte("seal:store_manage")
		})
	}
	ctx := context.Background()
	calls := map[string]func() error{
		"Put": func() error {
			return tr.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "jwt", Secret: []byte("s")})
		},
		"Get": func() error {
			_, err := tr.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "jwt"})
			return err
		},
		"Delete": func() error {
			return tr.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "jwt"})
		},
		"List": func() error {
			_, err := tr.SealStoreList(ctx)
			return err
		},
		"Reseal": func() error {
			return tr.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "jwt"})
		},
		"Status": func() error {
			_, err := tr.SealStoreStatus(ctx)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			var re *transport.RequestError
			if !errors.As(err, &re) {
				t.Fatalf("SealStore%s = %v, want a *transport.RequestError", name, err)
			}
			if re.StatusCode != int(wire.StatusPermissionDenied) {
				t.Fatalf("SealStore%s status = %d, want %d (permission denied)", name, re.StatusCode, wire.StatusPermissionDenied)
			}
		})
	}
}

// sealStoreRefusalFrame encodes the error frame the QUIC server sends for a
// seal-store error: the status, the message and the structured refusal
// reason errclass.Refusal attaches. The e2e harness frames every error body
// as plain message text, so the decoding is exercised on mapWireError
// directly.
func sealStoreRefusalFrame(t *testing.T, status uint16, sentinel error) []byte {
	t.Helper()
	reason, ok := transport.BarrierRefusal(sentinel)
	if !ok {
		t.Fatalf("BarrierRefusal(%v) has no reason", sentinel)
	}
	body, err := wire.EncodeErrorPayload(status, wire.ErrorPayload{
		Message: sentinel.Error(),
		Info:    &wire.ErrorInfo{Domain: transport.RefusalDomain, Reason: reason},
	})
	if err != nil {
		t.Fatalf("EncodeErrorPayload: %v", err)
	}
	return body
}

// TestE2E_SealStoreMissingSecretIsSealStoreNotFound proves a missing secret,
// an invalid name and a sealed seal-store barrier reach a QUIC caller as the
// seal sentinel the embedded transport returns (#1647), not as a
// KeyNotFoundError.
func TestE2E_SealStoreMissingSecretIsSealStoreNotFound(t *testing.T) {
	cases := []struct {
		method   string
		status   uint16
		sentinel error
	}{
		{"sealstore.Get", wire.StatusNotFound, seal.ErrSealStoreSecretNotFound},
		{"sealstore.Delete", wire.StatusNotFound, seal.ErrSealStoreSecretNotFound},
		{"sealstore.Put", wire.StatusInvalidArgument, seal.ErrInvalidSecretName},
		{"sealstore.Get", wire.StatusUnavailable, seal.ErrSealStoreBarrierSealed},
	}
	for _, tc := range cases {
		err := mapWireError(tc.method, tc.status, sealStoreRefusalFrame(t, tc.status, tc.sentinel))
		if !errors.Is(err, tc.sentinel) {
			t.Fatalf("%s %s = %v, want errors.Is %v", tc.method, wire.StatusName(tc.status), err, tc.sentinel)
		}
		if _, ok := errors.AsType[*transport.KeyNotFoundError](err); ok {
			t.Fatalf("%s %s = %v, want no KeyNotFoundError", tc.method, wire.StatusName(tc.status), err)
		}
	}
}

func TestE2E_SealStoreBadJSON(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("sealstore.Status", func([]byte) (uint16, []byte) {
		return wire.StatusOK, []byte("not json")
	})
	_, err := tr.SealStoreStatus(context.Background())
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("SealStoreStatus = %v, want an UnmarshalError", err)
	}
}

func TestE2E_SealStoreClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	if _, err := tr.SealStoreList(context.Background()); !errors.As(err, &ce) {
		t.Fatalf("SealStoreList = %T, want ClosedError", err)
	}
}

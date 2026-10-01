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
	"bytes"
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dekwire"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// dekStores resolves one in-memory store per tenant.
type dekStores map[string]*kvstore.MemoryKVStore

func (d dekStores) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	s, ok := d[tenantID]
	if !ok {
		s = kvstore.NewMemoryKVStore()
		d[tenantID] = s
	}
	return s, nil
}

// startDEKServer serves the dek.* methods from the production keyring
// service over the QUIC wire harness, framing replies exactly as
// pkg/api/quic does, and returns a connected SDK client.
func startDEKServer(t *testing.T) *Transport {
	t.Helper()
	keyring, err := dek.NewKeyring(dekStores{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.NewDEKService(keyring)
	if err != nil {
		t.Fatal(err)
	}
	srv, tr := startE2EServer(t)
	ctx := context.Background()
	reply := func(m proto.Message, err error) (uint16, []byte) {
		if err != nil {
			return wire.StatusFailedPrecondition, []byte(err.Error())
		}
		out, merr := proto.Marshal(m)
		if merr != nil {
			return wire.StatusError, []byte(merr.Error())
		}
		return wire.StatusOK, out
	}
	srv.SetResponder("dek.Encrypt", func(p []byte) (uint16, []byte) {
		var in pb.TenantDEKEncryptRequest
		if err := proto.Unmarshal(p, &in); err != nil {
			return wire.StatusInvalidArgument, []byte(err.Error())
		}
		s, err := svc.Encrypt(ctx, in.GetTenantId(), in.GetPlaintext(), in.GetAad())
		if err != nil {
			return reply(nil, err)
		}
		return reply(&pb.TenantDEKEncryptResponse{Ciphertext: s.Ciphertext, Version: s.Version}, nil)
	})
	srv.SetResponder("dek.Decrypt", func(p []byte) (uint16, []byte) {
		var in pb.TenantDEKDecryptRequest
		if err := proto.Unmarshal(p, &in); err != nil {
			return wire.StatusInvalidArgument, []byte(err.Error())
		}
		o, err := svc.Decrypt(ctx, in.GetTenantId(), in.GetCiphertext(), in.GetAad())
		if err != nil {
			return reply(nil, err)
		}
		return reply(&pb.TenantDEKDecryptResponse{Plaintext: o.Plaintext, Version: o.Version}, nil)
	})
	srv.SetResponder("dek.Rewrap", func(p []byte) (uint16, []byte) {
		var in pb.TenantDEKRewrapRequest
		if err := proto.Unmarshal(p, &in); err != nil {
			return wire.StatusInvalidArgument, []byte(err.Error())
		}
		r, err := svc.Rewrap(ctx, in.GetTenantId(), in.GetCiphertext(), in.GetAad())
		if err != nil {
			return reply(nil, err)
		}
		return reply(&pb.TenantDEKRewrapResponse{Ciphertext: r.Ciphertext, FromVersion: r.FromVersion, ToVersion: r.ToVersion}, nil)
	})
	statusResponder := func(op func(*pb.TenantDEKRetireRequest) (*dek.Status, error)) wireResponder {
		return func(p []byte) (uint16, []byte) {
			var in pb.TenantDEKRetireRequest // field-compatible with TenantDEKRequest
			if err := proto.Unmarshal(p, &in); err != nil {
				return wire.StatusInvalidArgument, []byte(err.Error())
			}
			st, err := op(&in)
			if err != nil {
				return reply(nil, err)
			}
			return reply(dekwire.StatusProto(st), nil)
		}
	}
	srv.SetResponder("dek.Status", statusResponder(func(in *pb.TenantDEKRetireRequest) (*dek.Status, error) {
		return svc.Status(ctx, in.GetTenantId())
	}))
	srv.SetResponder("dek.Rotate", statusResponder(func(in *pb.TenantDEKRetireRequest) (*dek.Status, error) {
		return svc.Rotate(ctx, in.GetTenantId())
	}))
	srv.SetResponder("dek.Retire", statusResponder(func(in *pb.TenantDEKRetireRequest) (*dek.Status, error) {
		return svc.Retire(ctx, in.GetTenantId(), in.GetVersion())
	}))
	return tr
}

func TestQUICDEK_LifecycleOverTheWire(t *testing.T) {
	tr := startDEKServer(t)
	ctx := context.Background()
	aad := []byte("path=db/password|v=1")

	enc, err := tr.TenantDEKEncrypt(ctx, &transport.TenantDEKEncryptRequest{Plaintext: []byte("s3cret"), AAD: aad})
	if err != nil || enc.Version != 1 {
		t.Fatalf("encrypt = %+v, %v", enc, err)
	}
	st, err := tr.TenantDEKRotate(ctx, &transport.TenantDEKRequest{})
	if err != nil || st.CurrentVersion != 2 {
		t.Fatalf("rotate = %+v, %v", st, err)
	}
	rw, err := tr.TenantDEKRewrap(ctx, &transport.TenantDEKRewrapRequest{Ciphertext: enc.Ciphertext, AAD: aad})
	if err != nil || rw.ToVersion != 2 {
		t.Fatalf("rewrap = %+v, %v", rw, err)
	}
	st, err = tr.TenantDEKRetire(ctx, &transport.TenantDEKRetireRequest{Version: 1})
	if err != nil || st.Versions[0].RetiredAt == nil {
		t.Fatalf("retire = %+v, %v", st, err)
	}
	dec, err := tr.TenantDEKDecrypt(ctx, &transport.TenantDEKDecryptRequest{Ciphertext: rw.Ciphertext, AAD: aad})
	if err != nil || !bytes.Equal(dec.Plaintext, []byte("s3cret")) || dec.Version != 2 {
		t.Fatalf("decrypt = %+v, %v", dec, err)
	}
	if st, err = tr.TenantDEKStatus(ctx, &transport.TenantDEKRequest{}); err != nil || len(st.Versions) != 2 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if _, err := tr.TenantDEKDecrypt(ctx, &transport.TenantDEKDecryptRequest{Ciphertext: enc.Ciphertext, AAD: aad}); err == nil {
		t.Fatal("a retired version must not decrypt")
	}
}

func TestQUICDEK_NilRequests(t *testing.T) {
	tr := startDEKServer(t)
	ctx := context.Background()
	checks := map[string]error{}
	_, checks["status"] = tr.TenantDEKStatus(ctx, nil)
	_, checks["rotate"] = tr.TenantDEKRotate(ctx, nil)
	_, checks["retire"] = tr.TenantDEKRetire(ctx, nil)
	_, checks["encrypt"] = tr.TenantDEKEncrypt(ctx, nil)
	_, checks["decrypt"] = tr.TenantDEKDecrypt(ctx, nil)
	_, checks["rewrap"] = tr.TenantDEKRewrap(ctx, nil)
	for op, err := range checks {
		var nre *transport.NilRequestError
		if !errors.As(err, &nre) {
			t.Errorf("%s(nil): %v", op, err)
		}
	}
}

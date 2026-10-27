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

package vlogconv

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"google.golang.org/grpc"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// fakeClient answers the Vlog* RPCs with canned responses, or with err, and
// records the last request. The embedded interface is nil: any other RPC
// panics, which is what a test that reaches one should do.
type fakeClient struct {
	pb.QRDBServiceClient
	err     error
	sigType pb.VlogSignatureType
	last    any
	noKey   bool
}

func (f *fakeClient) answer(req any, resp any) (any, error) {
	f.last = req
	if f.err != nil {
		return nil, f.err
	}
	return resp, nil
}

func (f *fakeClient) VlogCreate(_ context.Context, in *pb.VlogCreateRequest, _ ...grpc.CallOption) (*pb.VlogCreateResponse, error) {
	resp := &pb.VlogCreateResponse{Log: &pb.VlogLogID{TenantId: "acme", Database: "system", Log: in.GetLog()}}
	if !f.noKey {
		resp.Key = &pb.VlogHeadKey{Version: 1, Ref: "kref", PublicKey: []byte("pk")}
	}
	r, err := f.answer(in, resp)
	return as[pb.VlogCreateResponse](r), err
}

func (f *fakeClient) VlogList(_ context.Context, in *pb.VlogListRequest, _ ...grpc.CallOption) (*pb.VlogListResponse, error) {
	r, err := f.answer(in, &pb.VlogListResponse{Logs: []*pb.VlogLogID{{Log: "a"}, {Log: "b"}}})
	return as[pb.VlogListResponse](r), err
}

func (f *fakeClient) VlogAppend(_ context.Context, in *pb.VlogAppendRequest, _ ...grpc.CallOption) (*pb.VlogAppendResponse, error) {
	r, err := f.answer(in, &pb.VlogAppendResponse{FirstIndex: 4, Count: int64(len(in.GetEntries()))})
	return as[pb.VlogAppendResponse](r), err
}

func (f *fakeClient) VlogGet(_ context.Context, in *pb.VlogGetRequest, _ ...grpc.CallOption) (*pb.VlogGetResponse, error) {
	r, err := f.answer(in, &pb.VlogGetResponse{Entries: []*pb.VlogEntry{{Index: 1, Body: []byte("b")}, {Index: 2, Expired: true}}})
	return as[pb.VlogGetResponse](r), err
}

func (f *fakeClient) VlogProveInclusion(_ context.Context, in *pb.VlogProveInclusionRequest, _ ...grpc.CallOption) (*pb.VlogInclusionProof, error) {
	r, err := f.answer(in, &pb.VlogInclusionProof{LeafIndex: in.GetLeafIndex(), TreeSize: in.GetTreeSize(), Hashes: [][]byte{[]byte("h")}})
	return as[pb.VlogInclusionProof](r), err
}

func (f *fakeClient) VlogProveConsistency(_ context.Context, in *pb.VlogProveConsistencyRequest, _ ...grpc.CallOption) (*pb.VlogConsistencyProof, error) {
	r, err := f.answer(in, &pb.VlogConsistencyProof{Size1: in.GetSize1(), Size2: in.GetSize2(), Hashes: [][]byte{[]byte("h")}})
	return as[pb.VlogConsistencyProof](r), err
}

func (f *fakeClient) VlogHead(_ context.Context, in *pb.VlogHeadRequest, _ ...grpc.CallOption) (*pb.VlogHeadResponse, error) {
	r, err := f.answer(in, &pb.VlogHeadResponse{Size: 3, Root: []byte("r"), BodiesFrom: 1, Checkpoint: []byte("cp"), CheckpointSize: 2})
	return as[pb.VlogHeadResponse](r), err
}

func (f *fakeClient) VlogSignHead(_ context.Context, in *pb.VlogSignHeadRequest, _ ...grpc.CallOption) (*pb.VlogSignHeadResponse, error) {
	r, err := f.answer(in, &pb.VlogSignHeadResponse{
		Head:       &pb.VlogSignedHead{Size: 3, Root: []byte("r"), KeyVersion: 2, SignatureType: f.sigType, Signature: []byte("sig")},
		Checkpoint: []byte("note"),
	})
	return as[pb.VlogSignHeadResponse](r), err
}

func (f *fakeClient) VlogRotateKey(_ context.Context, in *pb.VlogRotateKeyRequest, _ ...grpc.CallOption) (*pb.VlogRotateKeyResponse, error) {
	r, err := f.answer(in, &pb.VlogRotateKeyResponse{Key: &pb.VlogHeadKey{Version: 2, Ref: "kref", PublicKey: []byte("pk2")}})
	return as[pb.VlogRotateKeyResponse](r), err
}

func (f *fakeClient) VlogExpireBodies(_ context.Context, in *pb.VlogExpireBodiesRequest, _ ...grpc.CallOption) (*pb.VlogExpireBodiesResponse, error) {
	r, err := f.answer(in, &pb.VlogExpireBodiesResponse{Expired: in.GetCutoff()})
	return as[pb.VlogExpireBodiesResponse](r), err
}

func as[T any](v any) *T {
	if v == nil {
		return nil
	}
	return v.(*T)
}

// mappedError marks an error that went through the caller's MapError.
type mappedError struct {
	method string
	err    error
}

func (e *mappedError) Error() string { return e.method + ": " + e.err.Error() }
func (e *mappedError) Unwrap() error { return e.err }

func newCaller(f *fakeClient) Caller {
	return Caller{Client: f, MapError: func(method string, err error) error { return &mappedError{method: method, err: err} }}
}

// calls drives every Caller method, with a nil request when nilReq is set.
func calls(c Caller, nilReq bool) map[string]error {
	ctx := context.Background()
	out := map[string]error{}
	if nilReq {
		_, out["VlogCreate"] = c.Create(ctx, nil)
		_, out["VlogAppend"] = c.Append(ctx, nil)
		_, out["VlogGet"] = c.Get(ctx, nil)
		_, out["VlogProveInclusion"] = c.ProveInclusion(ctx, nil)
		_, out["VlogProveConsistency"] = c.ProveConsistency(ctx, nil)
		_, out["VlogHead"] = c.Head(ctx, nil)
		_, out["VlogSignHead"] = c.SignHead(ctx, nil)
		_, out["VlogRotateKey"] = c.RotateKey(ctx, nil)
		_, out["VlogExpireBodies"] = c.ExpireBodies(ctx, nil)
		return out
	}
	_, out["VlogCreate"] = c.Create(ctx, &vlogwire.CreateRequest{Log: "l"})
	_, out["VlogList"] = c.List(ctx, nil)
	_, out["VlogAppend"] = c.Append(ctx, &vlogwire.AppendRequest{Log: "l"})
	_, out["VlogGet"] = c.Get(ctx, &vlogwire.GetRequest{Log: "l"})
	_, out["VlogProveInclusion"] = c.ProveInclusion(ctx, &vlogwire.InclusionRequest{Log: "l"})
	_, out["VlogProveConsistency"] = c.ProveConsistency(ctx, &vlogwire.ConsistencyRequest{Log: "l"})
	_, out["VlogHead"] = c.Head(ctx, &vlogwire.LogRequest{Log: "l"})
	_, out["VlogSignHead"] = c.SignHead(ctx, &vlogwire.LogRequest{Log: "l"})
	_, out["VlogRotateKey"] = c.RotateKey(ctx, &vlogwire.RotateKeyRequest{Log: "l"})
	_, out["VlogExpireBodies"] = c.ExpireBodies(ctx, &vlogwire.ExpireRequest{Log: "l"})
	return out
}

func TestCaller_ConvertsEveryRequestAndResult(t *testing.T) {
	f := &fakeClient{sigType: pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ECDSA_P256_SHA256}
	c := newCaller(f)
	ctx := context.Background()

	created, err := c.Create(ctx, &vlogwire.CreateRequest{Log: "l", Origin: "o", VerifierKeys: []string{"v"}, AllowExpireBodies: true, SigningKeyType: vlog.SignatureEd25519})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantCreate := &vlogwire.CreateResult{TenantID: "acme", Database: "system", Log: "l", Key: &vlogwire.HeadKey{Version: 1, PublicKey: []byte("pk")}}
	if !reflect.DeepEqual(created, wantCreate) {
		t.Fatalf("Create = %+v, want %+v", created, wantCreate)
	}
	if in := f.last.(*pb.VlogCreateRequest); in.GetOrigin() != "o" || in.GetVerifierKeys()[0] != "v" || !in.GetAllowExpireBodies() || in.GetSigningKeyType() != pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ED25519 {
		t.Fatalf("Create sent %+v", in)
	}

	list, err := c.List(ctx, &vlogwire.ListRequest{})
	if err != nil || !reflect.DeepEqual(list.Logs, []string{"a", "b"}) {
		t.Fatalf("List = %+v, %v", list, err)
	}

	appended, err := c.Append(ctx, &vlogwire.AppendRequest{Log: "l", Entries: [][]byte{[]byte("x"), []byte("y")}})
	if err != nil || *appended != (vlogwire.AppendResult{FirstIndex: 4, Count: 2}) {
		t.Fatalf("Append = %+v, %v", appended, err)
	}

	got, err := c.Get(ctx, &vlogwire.GetRequest{Log: "l", From: 1, Limit: 7})
	wantEntries := []vlogwire.Entry{{Index: 1, Body: []byte("b")}, {Index: 2, Expired: true}}
	if err != nil || !reflect.DeepEqual(got.Entries, wantEntries) {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if in := f.last.(*pb.VlogGetRequest); in.GetFrom() != 1 || in.GetLimit() != 7 {
		t.Fatalf("Get sent %+v", in)
	}

	incl, err := c.ProveInclusion(ctx, &vlogwire.InclusionRequest{Log: "l", Index: 2, Size: 5})
	if err != nil || incl.Index != 2 || incl.Size != 5 || len(incl.Hashes) != 1 {
		t.Fatalf("ProveInclusion = %+v, %v", incl, err)
	}

	cons, err := c.ProveConsistency(ctx, &vlogwire.ConsistencyRequest{Log: "l", Size1: 2, Size2: 5})
	if err != nil || cons.Size1 != 2 || cons.Size2 != 5 || len(cons.Hashes) != 1 {
		t.Fatalf("ProveConsistency = %+v, %v", cons, err)
	}

	head, err := c.Head(ctx, &vlogwire.LogRequest{Log: "l"})
	wantHead := &vlogwire.Head{Size: 3, Root: []byte("r"), BodiesFrom: 1, Checkpoint: []byte("cp"), CheckpointSize: 2}
	if err != nil || !reflect.DeepEqual(head, wantHead) {
		t.Fatalf("Head = %+v, %v", head, err)
	}

	signed, err := c.SignHead(ctx, &vlogwire.LogRequest{Log: "l"})
	wantSigned := &vlogwire.SignHeadResult{
		Head:       vlog.SignedHead{Size: 3, Root: []byte("r"), KeyVersion: 2, SignatureType: vlog.SignatureECDSAP256SHA256, Signature: []byte("sig")},
		Checkpoint: []byte("note"),
	}
	if err != nil || !reflect.DeepEqual(signed, wantSigned) {
		t.Fatalf("SignHead = %+v, %v", signed, err)
	}

	rotated, err := c.RotateKey(ctx, &vlogwire.RotateKeyRequest{Log: "l", SigningKeyType: vlog.SignatureECDSAP256SHA256})
	if err != nil || !reflect.DeepEqual(rotated.Key, vlogwire.HeadKey{Version: 2, PublicKey: []byte("pk2")}) {
		t.Fatalf("RotateKey = %+v, %v", rotated, err)
	}
	if in := f.last.(*pb.VlogRotateKeyRequest); in.GetSigningKeyType() != pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ECDSA_P256_SHA256 {
		t.Fatalf("RotateKey sent %+v", in)
	}

	expired, err := c.ExpireBodies(ctx, &vlogwire.ExpireRequest{Log: "l", Cutoff: 9})
	if err != nil || expired.Expired != 9 {
		t.Fatalf("ExpireBodies = %+v, %v", expired, err)
	}
}

func TestCaller_CreateWithoutAKeyAnswersNoKey(t *testing.T) {
	created, err := newCaller(&fakeClient{noKey: true}).Create(context.Background(), &vlogwire.CreateRequest{Log: "l"})
	if err != nil || created.Key != nil {
		t.Fatalf("Create = %+v, %v; want no key", created, err)
	}
}

func TestCaller_NilRequestIsRefusedBeforeTheRPC(t *testing.T) {
	f := &fakeClient{}
	for method, err := range calls(newCaller(f), true) {
		var re *transport.RequestError
		var nre *transport.NilRequestError
		if !errors.As(err, &re) || re.Method != method || !errors.As(err, &nre) {
			t.Errorf("%s: err = %v, want RequestError{%s, NilRequestError}", method, err, method)
		}
	}
	if f.last != nil {
		t.Fatalf("a nil request reached the client: %+v", f.last)
	}
}

func TestCaller_RPCErrorsGoThroughMapError(t *testing.T) {
	cause := errors.New("unavailable")
	for method, err := range calls(newCaller(&fakeClient{err: cause}), false) {
		var me *mappedError
		if !errors.As(err, &me) || me.method != method || !errors.Is(err, cause) {
			t.Errorf("%s: err = %v, want the cause mapped under %s", method, err, method)
		}
	}
}

func TestCaller_UnsupportedKeyTypeIsRefusedBeforeTheRPC(t *testing.T) {
	f := &fakeClient{}
	c := newCaller(f)
	_, createErr := c.Create(context.Background(), &vlogwire.CreateRequest{Log: "l", SigningKeyType: "rsa"})
	_, rotateErr := c.RotateKey(context.Background(), &vlogwire.RotateKeyRequest{Log: "l", SigningKeyType: "rsa"})
	for method, err := range map[string]error{"VlogCreate": createErr, "VlogRotateKey": rotateErr} {
		var re *transport.RequestError
		var uk *vlog.UnsupportedKeyTypeError
		if !errors.As(err, &re) || re.Method != method || !errors.As(err, &uk) || uk.Type != "rsa" {
			t.Errorf("%s: err = %v, want RequestError{UnsupportedKeyTypeError{rsa}}", method, err)
		}
	}
	if f.last != nil {
		t.Fatalf("an unsupported key type reached the client: %+v", f.last)
	}
}

func TestCaller_SignedHeadWithAnUnknownSignatureTypeIsRefused(t *testing.T) {
	_, err := newCaller(&fakeClient{}).SignHead(context.Background(), &vlogwire.LogRequest{Log: "l"})
	var uk *vlog.UnsupportedKeyTypeError
	if !errors.As(err, &uk) || uk.Type != pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_UNSPECIFIED.String() {
		t.Fatalf("SignHead err = %v, want UnsupportedKeyTypeError for UNSPECIFIED", err)
	}
}

func TestSignatureTypeMaps_AreInverseOverEverySignedType(t *testing.T) {
	for proto, sig := range signatureTypeFromProto {
		if signatureTypeToProto[sig] != proto {
			t.Errorf("%s maps to %v, back to %v", proto, sig, signatureTypeToProto[sig])
		}
	}
	if len(signatureTypeToProto) != len(signatureTypeFromProto)+1 {
		t.Fatalf("to-proto has %d types, from-proto %d; want exactly the unkeyed one more", len(signatureTypeToProto), len(signatureTypeFromProto))
	}
}

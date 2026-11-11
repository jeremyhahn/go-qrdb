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

package dekconv

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// dekClient is a QRDBServiceClient that answers the five tenant-DEK RPCs the
// way the server's gRPC handler does: it records the request it was sent and
// returns either the canned response or the canned error. Any other RPC
// panics through the nil embedded interface, so a Caller that reached for
// the wrong method fails loudly.
type dekClient struct {
	pb.QRDBServiceClient

	err error

	statusReq  *pb.TenantDEKRequest
	rotateReq  *pb.TenantDEKRequest
	retireReq  *pb.TenantDEKRetireRequest
	encryptReq *pb.TenantDEKEncryptRequest
	decryptReq *pb.TenantDEKDecryptRequest
	rewrapReq  *pb.TenantDEKRewrapRequest

	statusResp *pb.TenantDEKStatusResponse
}

var _ pb.QRDBServiceClient = (*dekClient)(nil)

func (c *dekClient) TenantDEKStatus(_ context.Context, in *pb.TenantDEKRequest, _ ...grpc.CallOption) (*pb.TenantDEKStatusResponse, error) {
	c.statusReq = in
	if c.err != nil {
		return nil, c.err
	}
	return c.statusResp, nil
}

func (c *dekClient) TenantDEKRotate(_ context.Context, in *pb.TenantDEKRequest, _ ...grpc.CallOption) (*pb.TenantDEKStatusResponse, error) {
	c.rotateReq = in
	if c.err != nil {
		return nil, c.err
	}
	return c.statusResp, nil
}

func (c *dekClient) TenantDEKRetire(_ context.Context, in *pb.TenantDEKRetireRequest, _ ...grpc.CallOption) (*pb.TenantDEKStatusResponse, error) {
	c.retireReq = in
	if c.err != nil {
		return nil, c.err
	}
	return c.statusResp, nil
}

func (c *dekClient) TenantDEKEncrypt(_ context.Context, in *pb.TenantDEKEncryptRequest, _ ...grpc.CallOption) (*pb.TenantDEKEncryptResponse, error) {
	c.encryptReq = in
	if c.err != nil {
		return nil, c.err
	}
	return &pb.TenantDEKEncryptResponse{Ciphertext: append([]byte("ct:"), in.GetPlaintext()...), Version: 4}, nil
}

func (c *dekClient) TenantDEKDecrypt(_ context.Context, in *pb.TenantDEKDecryptRequest, _ ...grpc.CallOption) (*pb.TenantDEKDecryptResponse, error) {
	c.decryptReq = in
	if c.err != nil {
		return nil, c.err
	}
	return &pb.TenantDEKDecryptResponse{Plaintext: bytes.TrimPrefix(in.GetCiphertext(), []byte("ct:")), Version: 4}, nil
}

func (c *dekClient) TenantDEKRewrap(_ context.Context, in *pb.TenantDEKRewrapRequest, _ ...grpc.CallOption) (*pb.TenantDEKRewrapResponse, error) {
	c.rewrapReq = in
	if c.err != nil {
		return nil, c.err
	}
	return &pb.TenantDEKRewrapResponse{Ciphertext: in.GetCiphertext(), FromVersion: 3, ToVersion: 4}, nil
}

// mappedError is what the test's MapError wraps a client error in, so a test
// can see both which method was named and that the original error survived.
type mappedError struct {
	method string
	err    error
}

func (e *mappedError) Error() string { return e.method + ": " + e.err.Error() }
func (e *mappedError) Unwrap() error { return e.err }

func newCaller(c *dekClient) Caller {
	return Caller{Client: c, MapError: func(method string, err error) error {
		return &mappedError{method: method, err: err}
	}}
}

// TestCallerMapsClientErrorsWithMethodName: every RPC failure goes through
// MapError under the RPC's own name, and the client's error is not lost.
func TestCallerMapsClientErrorsWithMethodName(t *testing.T) {
	wire := errors.New("unavailable")
	caller := newCaller(&dekClient{err: wire})
	ctx := context.Background()

	calls := map[string]func() error{
		"TenantDEKStatus": func() error {
			_, err := caller.Status(ctx, &transport.TenantDEKRequest{TenantID: "a"})
			return err
		},
		"TenantDEKRotate": func() error {
			_, err := caller.Rotate(ctx, &transport.TenantDEKRequest{TenantID: "a"})
			return err
		},
		"TenantDEKRetire": func() error {
			_, err := caller.Retire(ctx, &transport.TenantDEKRetireRequest{TenantID: "a", Version: 1})
			return err
		},
		"TenantDEKEncrypt": func() error {
			_, err := caller.Encrypt(ctx, &transport.TenantDEKEncryptRequest{TenantID: "a"})
			return err
		},
		"TenantDEKDecrypt": func() error {
			_, err := caller.Decrypt(ctx, &transport.TenantDEKDecryptRequest{TenantID: "a"})
			return err
		},
		"TenantDEKRewrap": func() error {
			_, err := caller.Rewrap(ctx, &transport.TenantDEKRewrapRequest{TenantID: "a"})
			return err
		},
	}
	for method, call := range calls {
		err := call()
		var me *mappedError
		if !errors.As(err, &me) || me.method != method || !errors.Is(err, wire) {
			t.Fatalf("%s: err = %v", method, err)
		}
	}
}

// TestCallerRefusesNilRequestsWithoutCalling: a nil request is a typed
// RequestError naming the RPC, and the client is never reached.
func TestCallerRefusesNilRequestsWithoutCalling(t *testing.T) {
	c := &dekClient{}
	caller := newCaller(c)
	ctx := context.Background()

	calls := map[string]func() error{
		"TenantDEKStatus": func() error { _, err := caller.Status(ctx, nil); return err },
		"TenantDEKRotate": func() error { _, err := caller.Rotate(ctx, nil); return err },
		"TenantDEKRetire": func() error { _, err := caller.Retire(ctx, nil); return err },
		"TenantDEKEncrypt": func() error {
			_, err := caller.Encrypt(ctx, nil)
			return err
		},
		"TenantDEKDecrypt": func() error {
			_, err := caller.Decrypt(ctx, nil)
			return err
		},
		"TenantDEKRewrap": func() error { _, err := caller.Rewrap(ctx, nil); return err },
	}
	for method, call := range calls {
		err := call()
		var re *transport.RequestError
		var ne *transport.NilRequestError
		if !errors.As(err, &re) || re.Method != method || !errors.As(err, &ne) {
			t.Fatalf("%s: err = %v", method, err)
		}
	}
	if c.statusReq != nil || c.rotateReq != nil || c.retireReq != nil || c.encryptReq != nil || c.decryptReq != nil || c.rewrapReq != nil {
		t.Fatal("a nil request reached the client")
	}
}

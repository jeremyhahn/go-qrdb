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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRequest_CeremonyDispatch drives the ceremony verbs through the generic
// Request dispatch: each writes its result through the response pointer, a
// request of the wrong type is refused, and the service's refusal comes back
// unchanged.
func TestRequest_CeremonyDispatch(t *testing.T) {
	ctx := context.Background()
	svc := &mockCeremonyService{}
	tr := newExtTransport(t, WithCeremonyService(svc), WithCeremonyCA(&mockCeremonyCA{}))

	var initOut *transport.CeremonyInitializeResponse
	if err := tr.Request(ctx, "CeremonyInitialize", &transport.CeremonyInitializeRequest{SOPin: "so-pin-1234", UserPin: "user-pin-1234", Threshold: 2}, &initOut); err != nil {
		t.Fatal(err)
	}
	if initOut == nil || initOut.State != "initialized" || initOut.ShareCount != 3 {
		t.Fatalf("initialize response = %+v", initOut)
	}
	if svc.lastInitOpts == nil || svc.lastInitOpts.SOPin != "so-pin-1234" || svc.lastInitOpts.Threshold != 2 {
		t.Fatalf("the service saw %+v, want the request's SO PIN and threshold", svc.lastInitOpts)
	}

	var statusOut *transport.CeremonyStatusResponse
	if err := tr.Request(ctx, "CeremonyStatus", nil, &statusOut); err != nil {
		t.Fatal(err)
	}
	if statusOut == nil || statusOut.State != "initialized" || statusOut.SPKIPin != "pin" {
		t.Fatalf("status response = %+v", statusOut)
	}

	var signOut *transport.SignCSRInitResponse
	if err := tr.Request(ctx, "CeremonySignCSR", &transport.SignCSRInitRequest{Username: "alice", SOPin: "so-pin-1234", CSRPEM: "csr"}, &signOut); err != nil {
		t.Fatal(err)
	}
	if signOut == nil || signOut.CertPEM != "CERT" {
		t.Fatalf("sign-csr response = %+v", signOut)
	}

	for _, method := range []string{"CeremonyInitialize", "CeremonySignCSR"} {
		var invalid *InvalidRequestError
		if err := tr.Request(ctx, method, "not-the-request", nil); !errors.As(err, &invalid) || invalid.Method != method {
			t.Errorf("%s with the wrong request type = %v, want InvalidRequestError", method, err)
		}
	}
}

// TestRequest_CeremonyDispatch_ServiceRefusals verifies a refusal by the
// ceremony service reaches the caller through the dispatch, for each verb.
func TestRequest_CeremonyDispatch_ServiceRefusals(t *testing.T) {
	ctx := context.Background()
	svc := &mockCeremonyService{
		initFn: func(context.Context, ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
			return nil, ceremony.ErrAlreadyInitialized
		},
		statusFn: func(context.Context) (*service.CeremonyStatusInfo, error) {
			return nil, ceremony.ErrNotInitializedOrEnrolling
		},
		signCSRFn: func(context.Context, string, string, string, string) (*service.CeremonySignCSRResult, error) {
			return nil, ceremony.ErrNotInitializedOrEnrolling
		},
	}
	tr := newExtTransport(t, WithCeremonyService(svc), WithCeremonyCA(&mockCeremonyCA{}))
	cases := map[string]struct {
		req  any
		want error
	}{
		"CeremonyInitialize": {&transport.CeremonyInitializeRequest{}, ceremony.ErrAlreadyInitialized},
		"CeremonyStatus":     {nil, ceremony.ErrNotInitializedOrEnrolling},
		"CeremonySignCSR":    {&transport.SignCSRInitRequest{Username: "alice"}, ceremony.ErrNotInitializedOrEnrolling},
	}
	for method, c := range cases {
		if err := tr.Request(ctx, method, c.req, nil); !errors.Is(err, c.want) {
			t.Errorf("%s = %v, want %v", method, err, c.want)
		}
	}
}

// TestCeremonySignCSR_NilRequest verifies a nil sign-csr request is refused
// as invalid, as an initialize is, rather than dereferenced -- directly and
// through the dispatch, where a typed nil passes the request type check.
func TestCeremonySignCSR_NilRequest(t *testing.T) {
	ctx := context.Background()
	tr := newExtTransport(t, WithCeremonyService(&mockCeremonyService{}), WithCeremonyCA(&mockCeremonyCA{}))
	var invalid *InvalidRequestError
	if _, err := tr.CeremonySignCSR(ctx, nil); !errors.As(err, &invalid) || invalid.Method != "CeremonySignCSR" {
		t.Fatalf("CeremonySignCSR(nil) = %v, want InvalidRequestError", err)
	}
	if err := tr.Request(ctx, "CeremonySignCSR", (*transport.SignCSRInitRequest)(nil), nil); !errors.As(err, &invalid) {
		t.Fatalf("dispatching a typed nil sign-csr request = %v, want InvalidRequestError", err)
	}
}

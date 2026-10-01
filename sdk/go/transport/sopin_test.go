// Copyright 2026 Jeremy Hahn
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

package transport

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestWithSOPIN_CopiesAndReleaseWipes(t *testing.T) {
	caller := []byte("1234")
	ctx, release := WithSOPIN(context.Background(), caller)
	caller[0] = 'X' // the caller wiping or reusing its buffer does not reach the SDK copy
	got, ok := SOPINFromContext(ctx)
	if !ok || string(got) != "1234" {
		t.Fatalf("SOPINFromContext = %q, %v; want the copy made at bind time", got, ok)
	}
	got[0] = 'Y' // a transport wiping its copy does not reach the held PIN
	if again, _ := SOPINFromContext(ctx); string(again) != "1234" {
		t.Fatalf("held PIN changed through a returned copy: %q", again)
	}

	held := ctx.Value(soPINKey{}).(*soPIN)
	buf := held.pin
	release()
	for i, b := range buf {
		if b != 0 {
			t.Fatalf("byte %d of the released PIN is %d, want 0", i, b)
		}
	}
	if _, ok := SOPINFromContext(ctx); ok {
		t.Fatal("a released PIN is still presented")
	}
	release() // idempotent
}

func TestSOPINFromContext_Unbound(t *testing.T) {
	if pin, ok := SOPINFromContext(context.Background()); ok || pin != nil {
		t.Fatalf("SOPINFromContext(unbound) = %q, %v", pin, ok)
	}
	ctx, release := WithSOPIN(context.Background(), nil)
	defer release()
	if _, ok := SOPINFromContext(ctx); ok {
		t.Fatal("an empty PIN is presented")
	}
}

func TestResolveSOPIN(t *testing.T) {
	bound, release := WithSOPIN(context.Background(), []byte("1234"))
	defer release()
	cases := []struct {
		name    string
		ctx     context.Context
		request string
		want    string
		refused bool
	}{
		{"nothing bound keeps the request's", context.Background(), "5678", "5678", false},
		{"nothing anywhere", context.Background(), "", "", false},
		{"bound fills an empty request", bound, "", "1234", false},
		{"bound and matching", bound, "1234", "1234", false},
		{"bound and different", bound, "5678", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveSOPIN(tc.ctx, "BarrierRekey", tc.request)
			var conflict *SOPINConflictError
			if tc.refused != errors.As(err, &conflict) {
				t.Fatalf("err = %v, refused want %v", err, tc.refused)
			}
			if got != tc.want {
				t.Fatalf("pin = %q, want %q", got, tc.want)
			}
			if tc.refused && (conflict.Method != "BarrierRekey" || strings.Contains(err.Error(), "5678") || strings.Contains(err.Error(), "1234")) {
				t.Fatalf("conflict error %q must name the method and never a PIN", err)
			}
		})
	}
}

type captureRT struct{ req *http.Request }

func (c *captureRT) RoundTrip(req *http.Request) (*http.Response, error) {
	c.req = req
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

func TestSOPINRoundTripper(t *testing.T) {
	base := &captureRT{}
	rt := SOPINRoundTripper(base)

	req, _ := http.NewRequest(http.MethodPost, "https://db.example.com/v1/barrier/rekey", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if base.req.Header.Get(SOPINHeader) != "" {
		t.Fatal("a request without a bound PIN carries the header")
	}

	ctx, release := WithSOPIN(context.Background(), []byte("1234"))
	defer release()
	req = req.WithContext(ctx)
	resp, err = rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if got := base.req.Header.Get(SOPINHeader); got != "1234" {
		t.Fatalf("header = %q, want the bound PIN", got)
	}
	if req.Header.Get(SOPINHeader) != "" {
		t.Fatal("the caller's request was mutated; the round tripper must clone")
	}
}

func TestSOPINHeaders(t *testing.T) {
	if SOPINHeaders(context.Background()) != nil {
		t.Fatal("headers without a bound PIN")
	}
	ctx, release := WithSOPIN(context.Background(), []byte("1234"))
	defer release()
	if got := SOPINHeaders(ctx)[SOPINHeader]; got != "1234" {
		t.Fatalf("header = %q", got)
	}
}

func TestSOPINClientInterceptors(t *testing.T) {
	ctx, release := WithSOPIN(context.Background(), []byte("1234"))
	defer release()
	pinOf := func(ctx context.Context) string {
		md, _ := metadata.FromOutgoingContext(ctx)
		if v := md.Get(SOPINMetadataKey); len(v) > 0 {
			return v[0]
		}
		return ""
	}

	var unaryPIN string
	err := SOPINUnaryClientInterceptor()(ctx, "/m", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			unaryPIN = pinOf(ctx)
			return nil
		})
	if err != nil || unaryPIN != "1234" {
		t.Fatalf("unary metadata = %q, %v", unaryPIN, err)
	}

	var streamPIN string
	_, err = SOPINStreamClientInterceptor()(context.Background(), &grpc.StreamDesc{}, nil, "/m",
		func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
			streamPIN = pinOf(ctx)
			return nil, nil
		})
	if err != nil || streamPIN != "" {
		t.Fatalf("unbound stream metadata = %q, %v; want none", streamPIN, err)
	}
	_, _ = SOPINStreamClientInterceptor()(ctx, &grpc.StreamDesc{}, nil, "/m",
		func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
			streamPIN = pinOf(ctx)
			return nil, nil
		})
	if streamPIN != "1234" {
		t.Fatalf("stream metadata = %q", streamPIN)
	}
}

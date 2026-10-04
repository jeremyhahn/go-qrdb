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

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// mustBind binds pin with WithSOPIN, failing the test if it cannot be sealed.
func mustBind(t *testing.T, pin string) (context.Context, func()) {
	t.Helper()
	ctx, release, err := WithSOPIN(context.Background(), []byte(pin))
	if err != nil {
		t.Fatalf("WithSOPIN: %v", err)
	}
	return ctx, release
}

// mustRead returns the PIN bound in ctx, failing the test on an open error.
func mustRead(t *testing.T, ctx context.Context) ([]byte, bool) {
	t.Helper()
	pin, ok, err := SOPINFromContext(ctx)
	if err != nil {
		t.Fatalf("SOPINFromContext: %v", err)
	}
	return pin, ok
}

func TestWithSOPIN_SealsACopyAndReleaseDestroys(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()
	caller := []byte("1234")
	ctx, release, err := WithSOPIN(context.Background(), caller)
	if err != nil {
		t.Fatalf("WithSOPIN: %v", err)
	}
	if string(caller) != "1234" {
		t.Fatalf("WithSOPIN consumed the caller's buffer: %q", caller)
	}
	caller[0] = 'X' // the caller wiping or reusing its buffer does not reach the SDK copy
	got, ok := mustRead(t, ctx)
	if !ok || string(got) != "1234" {
		t.Fatalf("SOPINFromContext = %q, %v; want the copy made at bind time", got, ok)
	}
	got[0] = 'Y' // a transport wiping its copy does not reach the held PIN
	if again, _ := mustRead(t, ctx); string(again) != "1234" {
		t.Fatalf("held PIN changed through a returned copy: %q", again)
	}

	// The SDK's copy is sealed in protected memory, and release destroys it.
	sealed := ctx.Value(soPINKey{}).(*soPIN).pin
	release()
	if buf, err := sealed.Open(ctx); err == nil {
		buf.Destroy()
		t.Fatal("the released PIN can still be opened")
	}
	if _, ok := mustRead(t, ctx); ok {
		t.Fatal("a released PIN is still presented")
	}
	release() // idempotent
}

func TestSOPINFromContext_Unbound(t *testing.T) {
	if pin, ok := mustRead(t, context.Background()); ok || pin != nil {
		t.Fatalf("SOPINFromContext(unbound) = %q, %v", pin, ok)
	}
	ctx, release, err := WithSOPIN(context.Background(), nil)
	if err != nil {
		t.Fatalf("WithSOPIN(nil): %v", err)
	}
	defer release()
	if _, ok := mustRead(t, ctx); ok {
		t.Fatal("an empty PIN is presented")
	}
}

// TestSOPINFromContext_UnopenablePINRefusesTheCall: a sealed PIN the SDK
// cannot open is an error every carrier returns, never a call sent without
// its PIN.
func TestSOPINFromContext_UnopenablePINRefusesTheCall(t *testing.T) {
	sealed, err := secure.NewSecret(context.Background(), []byte("1234"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	sealed.Destroy()
	// A held PIN whose secret is gone without release having run: the state a
	// failed Open reaches, which release itself never leaves behind.
	ctx := context.WithValue(context.Background(), soPINKey{}, &soPIN{pin: sealed})
	var nilSecret *secure.NilSecretError
	if _, _, err := SOPINFromContext(ctx); !errors.As(err, &nilSecret) {
		t.Fatalf("SOPINFromContext = %v, want *secure.NilSecretError", err)
	}
	if _, err := ResolveSOPIN(ctx, "BarrierSeal", ""); !errors.As(err, &nilSecret) {
		t.Fatalf("ResolveSOPIN = %v, want the open error", err)
	}
	called := false
	invoker := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		called = true
		return nil
	}
	if err := SOPINUnaryClientInterceptor("grpc", true)(ctx, "/m", nil, nil, nil, invoker); !errors.As(err, &nilSecret) || called {
		t.Fatalf("interceptor: err=%v called=%v, want refused before sending", err, called)
	}
	base := &captureRT{}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://db.example.com:8443/v1/barrier/seal", nil)
	resp, err := SOPINRoundTripper(base, "rest", "db.example.com:8443").RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.As(err, &nilSecret) || base.req != nil {
		t.Fatalf("round tripper: err=%v sent=%v, want refused before sending", err, base.req != nil)
	}
}

func TestResolveSOPIN(t *testing.T) {
	bound, release := mustBind(t, "1234")
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
	rt := SOPINRoundTripper(base, "rest", "db.example.com:8443")

	req, _ := http.NewRequest(http.MethodPost, "https://db.example.com:8443/v1/barrier/rekey", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if base.req.Header.Get(SOPINHeader) != "" {
		t.Fatal("a request without a bound PIN carries the header")
	}

	ctx, release := mustBind(t, "1234")
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

func TestSOPINRoundTripper_RefusesPlaintextAndForeignHops(t *testing.T) {
	ctx, release := mustBind(t, "1234")
	defer release()
	cases := []struct {
		name, url string
		tlsErr    bool
	}{
		{"http to the configured host", "http://db.example.com:8443/v1/barrier/rekey", true},
		{"http to another host", "http://other.example.com/v1/barrier/rekey", true},
		{"https to another host", "https://other.example.com/v1/barrier/rekey", false},
		{"https to another port", "https://db.example.com:9443/v1/barrier/rekey", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := &captureRT{}
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, tc.url, nil)
			resp, err := SOPINRoundTripper(base, "rest", "db.example.com:8443").RoundTrip(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if base.req != nil {
				t.Fatal("the refused request was sent")
			}
			var tlsErr *SOPINRequiresTLSError
			var hostErr *SOPINForeignHostError
			if tc.tlsErr && !errors.As(err, &tlsErr) {
				t.Fatalf("err = %v, want *SOPINRequiresTLSError", err)
			}
			if !tc.tlsErr && (!errors.As(err, &hostErr) || hostErr.Host == "") {
				t.Fatalf("err = %v, want *SOPINForeignHostError", err)
			}
			if strings.Contains(err.Error(), "1234") {
				t.Fatal("the error names the PIN")
			}
		})
	}
}

func TestConfiguredHost(t *testing.T) {
	if got := ConfiguredHost("https://db.example.com:8443/mcp"); got != "db.example.com:8443" {
		t.Fatalf("ConfiguredHost = %q", got)
	}
	if got := ConfiguredHost("://bad"); got != "" {
		t.Fatalf("ConfiguredHost(bad) = %q, want empty", got)
	}
}

func TestSOPINClientInterceptors(t *testing.T) {
	ctx, release := mustBind(t, "1234")
	defer release()
	pinOf := func(ctx context.Context) string {
		md, _ := metadata.FromOutgoingContext(ctx)
		if v := md.Get(SOPINMetadataKey); len(v) > 0 {
			return v[0]
		}
		return ""
	}

	var unaryPIN string
	err := SOPINUnaryClientInterceptor("grpc", true)(ctx, "/m", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			unaryPIN = pinOf(ctx)
			return nil
		})
	if err != nil || unaryPIN != "1234" {
		t.Fatalf("unary metadata = %q, %v", unaryPIN, err)
	}

	var streamPIN string
	_, err = SOPINStreamClientInterceptor("grpc", true)(context.Background(), &grpc.StreamDesc{}, nil, "/m",
		func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
			streamPIN = pinOf(ctx)
			return nil, nil
		})
	if err != nil || streamPIN != "" {
		t.Fatalf("unbound stream metadata = %q, %v; want none", streamPIN, err)
	}
	_, _ = SOPINStreamClientInterceptor("grpc", true)(ctx, &grpc.StreamDesc{}, nil, "/m",
		func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
			streamPIN = pinOf(ctx)
			return nil, nil
		})
	if streamPIN != "1234" {
		t.Fatalf("stream metadata = %q", streamPIN)
	}
}

func TestSOPINClientInterceptors_RefusePlaintext(t *testing.T) {
	ctx, release := mustBind(t, "1234")
	defer release()
	called := false
	err := SOPINUnaryClientInterceptor("unix", false)(ctx, "/m", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			called = true
			return nil
		})
	var tlsErr *SOPINRequiresTLSError
	if !errors.As(err, &tlsErr) || tlsErr.Transport != "unix" || called {
		t.Fatalf("unary over plaintext: err=%v called=%v, want refused before sending", err, called)
	}
	_, err = SOPINStreamClientInterceptor("unix", false)(ctx, &grpc.StreamDesc{}, nil, "/m",
		func(context.Context, *grpc.StreamDesc, *grpc.ClientConn, string, ...grpc.CallOption) (grpc.ClientStream, error) {
			called = true
			return nil, nil
		})
	if !errors.As(err, &tlsErr) || called {
		t.Fatalf("stream over plaintext: err=%v called=%v, want refused before sending", err, called)
	}
	// No bound PIN: plaintext RPCs pass as before.
	if err := SOPINUnaryClientInterceptor("unix", false)(context.Background(), "/m", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }); err != nil {
		t.Fatalf("unbound plaintext RPC refused: %v", err)
	}
}

func TestSOPINErrors_NeverNameThePIN(t *testing.T) {
	for _, err := range []error{
		&SOPINRequiresTLSError{Transport: "grpc"},
		&SOPINForeignHostError{Transport: "mcp", Host: "other.example.com"},
	} {
		if err.Error() == "" {
			t.Fatalf("%T: empty message", err)
		}
	}
}

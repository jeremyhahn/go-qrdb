// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// awaiting_admin_credential.go: the unary and stream gates
// ---------------------------------------------------------------------------

// cvgAwaitingProbe reports a fixed awaiting-admin-credential state.
type cvgAwaitingProbe struct{ awaiting bool }

func (p cvgAwaitingProbe) AwaitingAdminCredential() bool { return p.awaiting }

var _ awaitingAdminCredentialProbe = cvgAwaitingProbe{}

const (
	cvgAllowedMethod = "/qrdb.v1.QRDBService/Allowed"
	cvgDeniedMethod  = "/qrdb.v1.QRDBService/Denied"
)

var cvgAllowlist = map[string]struct{}{cvgAllowedMethod: {}}

// cvgUnaryChain records that the chain ran and answers "chain".
func cvgUnaryChain(ran *bool) grpc.UnaryServerInterceptor {
	return func(context.Context, any, *grpc.UnaryServerInfo, grpc.UnaryHandler) (any, error) {
		*ran = true
		return "chain", nil
	}
}

// cvgStreamChain records that the chain ran.
func cvgStreamChain(ran *bool) grpc.StreamServerInterceptor {
	return func(any, grpc.ServerStream, *grpc.StreamServerInfo, grpc.StreamHandler) error {
		*ran = true
		return nil
	}
}

func cvgUnaryHandler(context.Context, any) (any, error) { return "handler", nil }

func TestAwaitingAdminCredentialUnaryInterceptor(t *testing.T) {
	tests := []struct {
		name      string
		probe     awaitingAdminCredentialProbe
		method    string
		want      any
		wantChain bool
		wantCode  codes.Code
	}{
		{name: "nil probe runs the chain", probe: nil, method: cvgDeniedMethod, want: "chain", wantChain: true, wantCode: codes.OK},
		{name: "not awaiting runs the chain", probe: cvgAwaitingProbe{}, method: cvgDeniedMethod, want: "chain", wantChain: true, wantCode: codes.OK},
		{name: "awaiting rejects an unlisted method", probe: cvgAwaitingProbe{awaiting: true}, method: cvgDeniedMethod, want: nil, wantCode: codes.Unavailable},
		{name: "awaiting bypasses the chain for a listed method", probe: cvgAwaitingProbe{awaiting: true}, method: cvgAllowedMethod, want: "handler", wantCode: codes.OK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ran bool
			ic := AwaitingAdminCredentialUnaryInterceptor(tt.probe, cvgAllowlist, cvgUnaryChain(&ran))
			got, err := ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: tt.method}, cvgUnaryHandler)
			if code := status.Code(err); code != tt.wantCode {
				t.Fatalf("code = %v, want %v (err %v)", code, tt.wantCode, err)
			}
			if got != tt.want {
				t.Errorf("result = %v, want %v", got, tt.want)
			}
			if ran != tt.wantChain {
				t.Errorf("chain ran = %v, want %v", ran, tt.wantChain)
			}
		})
	}
}

func TestAwaitingAdminCredentialStreamInterceptor(t *testing.T) {
	tests := []struct {
		name        string
		probe       awaitingAdminCredentialProbe
		method      string
		wantChain   bool
		wantHandler bool
		wantCode    codes.Code
	}{
		{name: "nil probe runs the chain", probe: nil, method: cvgDeniedMethod, wantChain: true, wantCode: codes.OK},
		{name: "not awaiting runs the chain", probe: cvgAwaitingProbe{}, method: cvgDeniedMethod, wantChain: true, wantCode: codes.OK},
		{name: "awaiting rejects an unlisted method", probe: cvgAwaitingProbe{awaiting: true}, method: cvgDeniedMethod, wantCode: codes.Unavailable},
		{name: "awaiting bypasses the chain for a listed method", probe: cvgAwaitingProbe{awaiting: true}, method: cvgAllowedMethod, wantHandler: true, wantCode: codes.OK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ran, handled bool
			ic := AwaitingAdminCredentialStreamInterceptor(tt.probe, cvgAllowlist, cvgStreamChain(&ran))
			err := ic(nil, nil, &grpc.StreamServerInfo{FullMethod: tt.method}, func(any, grpc.ServerStream) error {
				handled = true
				return nil
			})
			if code := status.Code(err); code != tt.wantCode {
				t.Fatalf("code = %v, want %v (err %v)", code, tt.wantCode, err)
			}
			if ran != tt.wantChain {
				t.Errorf("chain ran = %v, want %v", ran, tt.wantChain)
			}
			if handled != tt.wantHandler {
				t.Errorf("handler ran = %v, want %v", handled, tt.wantHandler)
			}
		})
	}
}

// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
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

// ---------------------------------------------------------------------------
// error_status.go: ErrorClass of the marshal and decode errors
// ---------------------------------------------------------------------------

func TestErrorClass_MarshalAndDecodeErrors(t *testing.T) {
	inner := errors.New("boom")
	tests := []struct {
		name string
		err  interface{ ErrorClass() errclass.Class }
		want errclass.Class
	}{
		{name: "metadata marshal", err: &MetadataMarshalError{Err: inner}, want: errclass.Internal},
		{name: "document marshal", err: &DocumentMarshalError{Err: inner}, want: errclass.Internal},
		{name: "signature decode", err: &SignatureDecodeError{Err: inner}, want: errclass.InvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.ErrorClass(); got != tt.want {
				t.Errorf("ErrorClass() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// register.go: RegisterOptions that attach a dependency
// ---------------------------------------------------------------------------

// Each stub embeds its interface, so it satisfies it without implementing a
// method; the tests compare identity and never call through it.
type (
	cvgEncMigrator      struct{ service.EncryptionMigrator }
	cvgDEKService       struct{ service.DEKServicer }
	cvgCertEnrollment   struct{ service.CertEnrollmentServicer }
	cvgInvitation       struct{ service.InvitationServicer }
	cvgTenantOnboarding struct {
		service.TenantOnboardingServicer
	}
	cvgSQLService struct{ service.SQLServicer }
	cvgRegistry   struct{ service.Registry }
)

func TestRegisterOptions_AttachDependency(t *testing.T) {
	encMigrator := &cvgEncMigrator{}
	dekSvc := &cvgDEKService{}
	certEnroll := &cvgCertEnrollment{}
	invitationSvc := &cvgInvitation{}
	onboarding := &cvgTenantOnboarding{}
	mw := &authmw.AuthMiddleware{}
	sqlSvc := &cvgSQLService{}
	registry := &cvgRegistry{}

	tests := []struct {
		name string
		opt  RegisterOption
		got  func(*qrdbServiceServer) any
		want any
	}{
		{"encryption migrator", WithRegisterEncryptionMigrator(encMigrator), func(s *qrdbServiceServer) any { return s.encMigrator }, encMigrator},
		{"dek service", WithRegisterDEKService(dekSvc), func(s *qrdbServiceServer) any { return s.dekSvc }, dekSvc},
		{"cert enrollment", WithRegisterCertEnrollment(certEnroll), func(s *qrdbServiceServer) any { return s.certEnroll }, certEnroll},
		{"invitation service", WithRegisterInvitationService(invitationSvc), func(s *qrdbServiceServer) any { return s.invitationSvc }, invitationSvc},
		{"tenant onboarding", WithRegisterTenantOnboarding(onboarding), func(s *qrdbServiceServer) any { return s.onboarding }, onboarding},
		{"auth middleware", WithRegisterAuthMiddleware(mw), func(s *qrdbServiceServer) any { return s.authMW }, mw},
		{"sql service", WithRegisterSQLService(sqlSvc), func(s *qrdbServiceServer) any { return s.sqlSvc }, sqlSvc},
		{"service registry", WithRegisterServiceRegistry(registry), func(s *qrdbServiceServer) any { return s.registry }, registry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &qrdbServiceServer{}
			tt.opt(s)
			if got := tt.got(s); got != tt.want {
				t.Errorf("field = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRegisterOptions_RegisterService(t *testing.T) {
	grpcSrv := grpc.NewServer()
	defer grpcSrv.Stop()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterEncryptionMigrator(&cvgEncMigrator{}),
		WithRegisterDEKService(&cvgDEKService{}),
		WithRegisterCertEnrollment(&cvgCertEnrollment{}),
		WithRegisterInvitationService(&cvgInvitation{}),
		WithRegisterTenantOnboarding(&cvgTenantOnboarding{}),
		WithRegisterAuthMiddleware(&authmw.AuthMiddleware{}),
		WithRegisterSQLService(&cvgSQLService{}),
		WithRegisterServiceRegistry(&cvgRegistry{}),
	)
	if _, ok := grpcSrv.GetServiceInfo()[pb.QRDBService_ServiceDesc.ServiceName]; !ok {
		t.Fatalf("QRDBService not registered: %v", grpcSrv.GetServiceInfo())
	}
}

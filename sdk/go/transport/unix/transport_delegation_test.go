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

package unix

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestUnixRT_IssueDelegation(t *testing.T) {
	notAfter := time.Unix(1800000000, 0).UTC()
	var gotReq *service.IssueDelegationRequest
	enroll := &mockCertEnrollmentSvc{
		delegFn: func(_ context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			gotReq = req
			return &service.IssuedDelegation{
				TenantID: "t1", Principal: req.Principal, Scope: req.Scope,
				CertPEM: []byte("-----BEGIN CERTIFICATE-----\n"), Serial: "d1", NotAfter: notAfter,
			}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCertEnrollment(enroll))
	defer cleanup()

	got, err := tr.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr-bytes", TTL: "15m",
	})
	if err != nil {
		t.Fatalf("IssueDelegation: %v", err)
	}
	if got.Principal != "billing" || got.Serial != "d1" || !slices.Equal(got.Scope, []string{"kv:get"}) || !got.NotAfter.Equal(notAfter) {
		t.Errorf("got %+v", got)
	}
	if gotReq == nil || gotReq.Principal != "billing" || string(gotReq.CSRPEM) != "csr-bytes" || gotReq.TTL != 15*time.Minute {
		t.Errorf("server received %+v; want Principal=billing CSRPEM=csr-bytes TTL=15m", gotReq)
	}
}

func TestUnixRT_IssueDelegation_Refused(t *testing.T) {
	enroll := &mockCertEnrollmentSvc{
		delegFn: func(context.Context, *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			return nil, &service.DelegationDeniedError{Reason: "a delegated caller cannot delegate"}
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCertEnrollment(enroll))
	defer cleanup()

	_, err := tr.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr",
	})
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != int(codes.PermissionDenied) {
		t.Fatalf("err = %v (%T); want a *transport.RequestError carrying PermissionDenied", err, err)
	}
}

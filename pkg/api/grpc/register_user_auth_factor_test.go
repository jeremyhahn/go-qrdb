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

package grpc

import (
	"testing"

	"google.golang.org/grpc"
)

// TestWithRegisterUserAuthFactorService_RegistersSeparateService proves the
// option causes RegisterService to additionally register the standalone
// qrdb.v1.UserAuthFactorService alongside qrdb.v1.QRDBService — the wiring
// path composers (go-quicbaas) use to expose the user-login factor surface
// with a single RegisterService call.
func TestWithRegisterUserAuthFactorService_RegistersSeparateService(t *testing.T) {
	grpcSrv := grpc.NewServer()
	defer grpcSrv.Stop()

	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterUserAuthFactorService(newMockUserAuthFactorSvc()),
	)

	info := grpcSrv.GetServiceInfo()
	if _, ok := info["qrdb.v1.UserAuthFactorService"]; !ok {
		t.Fatalf("qrdb.v1.UserAuthFactorService must be registered; got services: %v", serviceNames(info))
	}
	if _, ok := info["qrdb.v1.QRDBService"]; !ok {
		t.Fatalf("qrdb.v1.QRDBService must still be registered; got services: %v", serviceNames(info))
	}
}

// TestWithRegisterUserAuthFactorService_NilServiceSkipsRegistration proves a
// nil servicer (or an omitted option) leaves the separate service
// unregistered so callers receive the transport's Unimplemented handling
// instead of a nil-receiver panic.
func TestWithRegisterUserAuthFactorService_NilServiceSkipsRegistration(t *testing.T) {
	grpcSrv := grpc.NewServer()
	defer grpcSrv.Stop()

	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterUserAuthFactorService(nil),
	)

	if _, ok := grpcSrv.GetServiceInfo()["qrdb.v1.UserAuthFactorService"]; ok {
		t.Fatal("nil servicer must not register qrdb.v1.UserAuthFactorService")
	}
}

// serviceNames flattens the registered gRPC service map keys for failure
// messages.
func serviceNames(info map[string]grpc.ServiceInfo) []string {
	names := make([]string, 0, len(info))
	for name := range info {
		names = append(names, name)
	}
	return names
}

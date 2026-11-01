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

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// TestWithCredentialService_StoresService asserts the option stores the
// credential service it was given on the Server.
func TestWithCredentialService_StoresService(t *testing.T) {
	cred := &stubCredentialService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithCredentialService(cred))
	if srv.credentialSvc != cred {
		t.Fatalf("credentialSvc = %v, want %v", srv.credentialSvc, cred)
	}
}

// TestWithAuthMiddleware_StoresMiddleware asserts the option stores the auth
// middleware it was given on the Server.
func TestWithAuthMiddleware_StoresMiddleware(t *testing.T) {
	mw := &authmw.AuthMiddleware{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithAuthMiddleware(mw))
	if srv.authMW != mw {
		t.Fatalf("authMW = %p, want %p", srv.authMW, mw)
	}
}

// TestWithUserAuthFactorService_StoresService asserts the option stores the
// user auth-factor service it was given on the Server.
func TestWithUserAuthFactorService_StoresService(t *testing.T) {
	svc := &stubUserAuthFactor{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithUserAuthFactorService(svc))
	if srv.userAuthFactorSvc != svc {
		t.Fatalf("userAuthFactorSvc = %v, want %v", srv.userAuthFactorSvc, svc)
	}
}

// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package grpc

import "testing"

// WithCredentialService is the one ServerOption no served-RPC test in this
// package passes; the other options are exercised by the handler suites.
func TestWithCredentialService_StoresService(t *testing.T) {
	cred := &stubCredentialService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithCredentialService(cred))
	if srv.credentialSvc != cred {
		t.Fatalf("credentialSvc = %v, want %v", srv.credentialSvc, cred)
	}
}

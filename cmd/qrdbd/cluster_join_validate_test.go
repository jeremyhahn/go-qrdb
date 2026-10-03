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

package main

import (
	"errors"
	"testing"
)

func TestValidateClusterJoinFlags_EnabledWithoutSealRefused(t *testing.T) {
	f := resolveWithArgs(t, "--cluster-join-enabled")

	err := validateClusterJoinFlags(f)
	var sealErr *ClusterJoinRequiresSealError
	if !errors.As(err, &sealErr) {
		t.Fatalf("validateClusterJoinFlags with no seal configured = %v, want *ClusterJoinRequiresSealError", err)
	}
}

func TestValidateClusterJoinFlags_EnabledWithSealOK(t *testing.T) {
	f := resolveWithArgs(t, "--cluster-join-enabled")
	f.sealConfig.Enabled = true

	if err := validateClusterJoinFlags(f); err != nil {
		t.Fatalf("validateClusterJoinFlags with seal configured = %v, want nil", err)
	}
}

func TestValidateClusterJoinFlags_DisabledNeedsNoSeal(t *testing.T) {
	f := resolveWithArgs(t)

	if err := validateClusterJoinFlags(f); err != nil {
		t.Fatalf("validateClusterJoinFlags with cluster join disabled = %v, want nil", err)
	}
}

func TestValidateClusterJoinFlags_HTTPJoinURLRefused(t *testing.T) {
	f := resolveWithArgs(t, "--bootstrap-join-url", "http://member1.example.com:8443")

	err := validateClusterJoinFlags(f)
	var httpsErr *JoinHTTPURLRequiresHTTPSError
	if !errors.As(err, &httpsErr) {
		t.Fatalf("validateClusterJoinFlags with an http:// join URL = %v, want *JoinHTTPURLRequiresHTTPSError", err)
	}
}

func TestValidateClusterJoinFlags_HTTPSJoinURLOK(t *testing.T) {
	f := resolveWithArgs(t, "--bootstrap-join-url", "https://member1.example.com:8443")

	if err := validateClusterJoinFlags(f); err != nil {
		t.Fatalf("validateClusterJoinFlags with an https:// join URL = %v, want nil", err)
	}
}

func TestClusterJoinRequiresSealError_ErrorString(t *testing.T) {
	err := &ClusterJoinRequiresSealError{}
	if err.Error() == "" {
		t.Fatal("ClusterJoinRequiresSealError.Error() is empty")
	}
}

func TestJoinHTTPURLRequiresHTTPSError_ErrorString(t *testing.T) {
	err := &JoinHTTPURLRequiresHTTPSError{URL: "http://x"}
	if err.Error() == "" {
		t.Fatal("JoinHTTPURLRequiresHTTPSError.Error() is empty")
	}
}

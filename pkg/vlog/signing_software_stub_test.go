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

//go:build !qrdb_vlog

package vlog

import (
	"context"
	"errors"
	"testing"
)

func TestStubSoftwareHeadSignerProvider_RefusesNotCompiled(t *testing.T) {
	p := NewSoftwareHeadSignerProvider(nil)
	var nce *NotCompiledError
	s, err := p.HeadSigner(context.Background(), HeadSignerRequest{KeyRef: KeyRef(FirstKeyVersion)})
	if s != nil || !errors.As(err, &nce) {
		t.Fatalf("HeadSigner = %v, %v; want *NotCompiledError", s, err)
	}
}

func TestStubSoftwareHeadSignerProvider_CreateKeyRefusesNotCompiled(t *testing.T) {
	var nce *NotCompiledError
	k, err := NewSoftwareHeadSignerProvider(nil).CreateKey(context.Background(),
		CreateKeyRequest{TenantID: testLog.TenantID, Database: testLog.Database, Log: testLog.Log, Type: SignatureEd25519, Version: FirstKeyVersion})
	if k.Record != nil || k.Public != nil || !errors.As(err, &nce) {
		t.Fatalf("CreateKey = %+v, %v; want *NotCompiledError", k, err)
	}
}

func TestStubSoftwareHeadSignerProvider_CreateKeyRefusesEveryType(t *testing.T) {
	for _, st := range signatureTypes {
		var nce *NotCompiledError
		if _, err := NewSoftwareHeadSignerProvider(nil).CreateKey(context.Background(), CreateKeyRequest{Type: st, Version: 2}); !errors.As(err, &nce) {
			t.Fatalf("CreateKey(%s) = %v; want *NotCompiledError", st, err)
		}
	}
}

func TestStubNewSoftwareHeadSigner_RefusesNotCompiled(t *testing.T) {
	var nce *NotCompiledError
	s, err := NewSoftwareHeadSigner("n", mustKey(t, SignatureEd25519))
	if s != nil || !errors.As(err, &nce) {
		t.Fatalf("NewSoftwareHeadSigner = %v, %v; want *NotCompiledError", s, err)
	}
}

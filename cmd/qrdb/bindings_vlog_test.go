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
	"strings"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// The VlogBinding round trip against a working log service is in
// bindings_vlog_service_test.go, behind the qrdb_vlog tag of that service.

// TestVlogBinding_NotConnected fails every method before a connection.
func TestVlogBinding_NotConnected(t *testing.T) {
	b := NewVlogBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	calls := map[string]func() error{
		"Create": func() error {
			_, err := b.Create(&vlogwire.CreateRequest{Log: "l", Origin: "example.com/l"})
			return err
		},
		"List":   func() error { _, err := b.List(); return err },
		"Append": func() error { _, err := b.Append(&vlogwire.AppendRequest{Log: "l"}); return err },
		"Get":    func() error { _, err := b.Get(&vlogwire.GetRequest{Log: "l"}); return err },
		"ProveInclusion": func() error {
			_, err := b.ProveInclusion(&vlogwire.InclusionRequest{Log: "l"})
			return err
		},
		"ProveConsistency": func() error {
			_, err := b.ProveConsistency(&vlogwire.ConsistencyRequest{Log: "l"})
			return err
		},
		"Head":      func() error { _, err := b.Head(&vlogwire.LogRequest{Log: "l"}); return err },
		"SignHead":  func() error { _, err := b.SignHead(&vlogwire.LogRequest{Log: "l"}); return err },
		"RotateKey": func() error { _, err := b.RotateKey(&vlogwire.RotateKeyRequest{Log: "l"}); return err },
		"ExpireBodies": func() error {
			_, err := b.ExpireBodies(&vlogwire.ExpireRequest{Log: "l"})
			return err
		},
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s succeeded without a connection", name)
		}
	}
}

// TestVlogBinding_CreateRequiresOrigin pins that the desktop binding refuses
// an empty origin with the service's error and never derives a default.
func TestVlogBinding_CreateRequiresOrigin(t *testing.T) {
	b := NewVlogBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	for name, req := range map[string]*vlogwire.CreateRequest{
		"nil request":  nil,
		"empty origin": {Log: "events"},
	} {
		_, err := b.Create(req)
		var ide *sdk.VlogInvalidDescriptorError
		if !errors.As(err, &ide) {
			t.Fatalf("%s: err = %v, want *sdk.VlogInvalidDescriptorError", name, err)
		}
		if !strings.Contains(err.Error(), "origin must be non-empty") {
			t.Errorf("%s: err = %q, want the service's origin refusal", name, err)
		}
	}
}

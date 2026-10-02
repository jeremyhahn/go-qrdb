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

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// A per-database Shamir barrier is not built yet, so a create asking for one
// is refused -- and refused as the caller's error (InvalidArgument, 400 on
// REST), not as a server fault, on every transport that classifies through
// errclass. The wizard does not offer it until per-database share holders
// ship; this is what any other client meets.
func TestDatabaseBinding_CreateWithShamirBarrierIsRefused(t *testing.T) {
	svc := barrierDatabaseService(t)
	b := NewDatabaseBinding(testLogger(t), newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
		embedded.WithDatabaseService(svc)))

	_, err := b.Create(&transport.CreateDatabaseRequest{
		Name:    "shamir-db",
		Engine:  "kv",
		Backend: "pebble",
		DataDir: t.TempDir(),
		Barrier: &transport.DatabaseBarrierConfig{Enabled: true, Mode: "shamir", SharesRequired: 2, SharesTotal: 3},
	})
	var unsupported *database.UnsupportedBarrierStrategyError
	if !errors.As(err, &unsupported) || unsupported.Mode != "shamir" {
		t.Fatalf("create with a Shamir barrier = %v, want *database.UnsupportedBarrierStrategyError", err)
	}
	if got := errclass.Classify(err); got != errclass.InvalidArgument {
		t.Fatalf("a Shamir barrier refusal classifies as %v, want InvalidArgument", got)
	}
	if _, getErr := svc.Get("shamir-db"); getErr == nil {
		t.Fatal("the refused create left the database behind")
	}
}

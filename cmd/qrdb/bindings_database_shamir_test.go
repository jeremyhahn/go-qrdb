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

// A create's barrier is refused as the caller's error, not a server fault, on
// every transport that classifies through errclass, and the refused create
// leaves no database behind:
//
//   - a strategy per-database barriers do not implement is InvalidArgument
//     (*database.UnsupportedBarrierStrategyError);
//   - a Shamir barrier on a node whose barrier manager has no share-holder
//     custody wired is FailedPrecondition
//     (*database.ShamirCustodyNotConfiguredError). Per-database Shamir is
//     served where custody is wired (docs/databases); the desktop wizard does
//     not offer it, so this is what any other client meets on such a node.
func TestDatabaseBinding_CreateWithAnUnservedBarrierIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		barrier *transport.DatabaseBarrierConfig
		want    errclass.Class
		check   func(error) bool
	}{
		"unknown strategy": {
			barrier: &transport.DatabaseBarrierConfig{Enabled: true, Mode: "tpm"},
			want:    errclass.InvalidArgument,
			check: func(err error) bool {
				var unsupported *database.UnsupportedBarrierStrategyError
				return errors.As(err, &unsupported) && unsupported.Mode == "tpm"
			},
		},
		"shamir without custody": {
			barrier: &transport.DatabaseBarrierConfig{Enabled: true, Mode: "shamir", SharesRequired: 2, SharesTotal: 3},
			want:    errclass.FailedPrecondition,
			check: func(err error) bool {
				var custody *database.ShamirCustodyNotConfiguredError
				return errors.As(err, &custody)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc := barrierDatabaseService(t)
			b := NewDatabaseBinding(testLogger(t), newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
				embedded.WithDatabaseService(svc)))
			_, err := b.Create(&transport.CreateDatabaseRequest{
				Name:    "refused-db",
				Engine:  "kv",
				Backend: "pebble",
				DataDir: t.TempDir(),
				Barrier: tc.barrier,
			})
			if !tc.check(err) {
				t.Fatalf("create = %v (%T)", err, err)
			}
			if got := errclass.Classify(err); got != tc.want {
				t.Fatalf("the refusal classifies as %v, want %v", got, tc.want)
			}
			if _, getErr := svc.Get("refused-db"); getErr == nil {
				t.Fatal("the refused create left the database behind")
			}
		})
	}
}

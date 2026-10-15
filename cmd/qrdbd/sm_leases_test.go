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
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestBuildSMCreateFunc_RegistryTypesKeepTheNodesLeases: every registry
// sm_type is built with the node's lessor, so the leader's expiry sweeper,
// which watches that lessor, sees their leases; a type that cannot keep
// leases is refused at start with SMLeasesUnsupportedError, naming the
// remedy, rather than run without the lease-backed membership a cluster needs.
func TestBuildSMCreateFunc_RegistryTypesKeepTheNodesLeases(t *testing.T) {
	for _, smType := range []string{"memory", "standard", "concurrent", "highperf"} {
		t.Run(smType, func(t *testing.T) {
			lr := lease.NewLessor()
			f := &startFlags{smType: smType, dataDir: t.TempDir()}
			createFn, err := buildSMCreateFunc(f, atrest.New(nil), lr, watch.NewHub())
			if err != nil {
				t.Fatalf("buildSMCreateFunc(%s): %v", smType, err)
			}
			machine := createFn(1, 1)
			reader, ok := machine.(interface {
				Lookup(context.Context, interface{}) (interface{}, error)
			})
			if !ok {
				t.Fatalf("%T exposes no Lookup", machine)
			}
			if _, err := lr.Grant(nopLeaseWriter{}, 30); err != nil {
				t.Fatal(err)
			}
			leases, err := reader.Lookup(context.Background(), lease.LeasesQuery{})
			if err != nil || len(leases.([]*lease.Lease)) != 1 {
				t.Fatalf("the state machine does not read the node's lessor: %v, %v", leases, err)
			}
		})
	}

	f := &startFlags{smType: "regular", dataDir: t.TempDir()}
	_, err := buildSMCreateFunc(f, atrest.New(nil), lease.NewLessor(), watch.NewHub())
	var unsupported *SMLeasesUnsupportedError
	if !errors.As(err, &unsupported) || unsupported.SMType != "regular" {
		t.Fatalf("buildSMCreateFunc(regular) = %v; want *SMLeasesUnsupportedError", err)
	}
	var notKV *statemachine.KVOptionsNotApplicableError
	if !errors.As(err, &notKV) || !strings.Contains(err.Error(), "--sm standard") {
		t.Fatalf("the refusal %q does not carry the registry's cause and the remedy", err)
	}
}

// nopLeaseWriter discards lease-table writes: the test grants straight into
// the lessor to see whether the state machine reads the same one.
type nopLeaseWriter struct{}

func (nopLeaseWriter) Set(_, _ []byte) error { return nil }
func (nopLeaseWriter) Delete(_ []byte) error { return nil }

var _ lease.Writer = nopLeaseWriter{}

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

package service

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// writeVerbs names every method on KVServicer that can store or remove data,
// with a call that reaches the write path.
//
// The set is checked against the interface below rather than trusted, because
// the property under test is only worth as much as its coverage: a verb added
// to KVServicer and forgotten here would be an unclassified write path, which
// is the exact thing this file exists to prevent.
var writeVerbs = map[string]func(context.Context, KVServicer) error{
	"Put": func(ctx context.Context, s KVServicer) error {
		return s.Put(ctx, "k", []byte("v"))
	},
	"PutIfAbsent": func(ctx context.Context, s KVServicer) error {
		_, err := s.PutIfAbsent(ctx, "k", []byte("v"))
		return err
	},
	"CompareAndSwap": func(ctx context.Context, s KVServicer) error {
		_, err := s.CompareAndSwap(ctx, "k", []byte("old"), []byte("new"))
		return err
	},
	"CompareAndDelete": func(ctx context.Context, s KVServicer) error {
		_, err := s.CompareAndDelete(ctx, "k", []byte("old"))
		return err
	},
	"Delete": func(ctx context.Context, s KVServicer) error {
		return s.Delete(ctx, "k")
	},
	"PutWithMode": func(ctx context.Context, s KVServicer) error {
		return s.PutWithMode(ctx, "k", []byte("v"), writemode.Sync)
	},
	"DeleteWithMode": func(ctx context.Context, s KVServicer) error {
		return s.DeleteWithMode(ctx, "k", writemode.Sync)
	},
	"PutEntity": func(ctx context.Context, s KVServicer) error {
		_, err := s.PutEntity(ctx, &PutEntityRequest{
			EntityType: "user",
			ID:         "1",
			Data:       []byte("v"),
		})
		return err
	},
	"DeleteEntity": func(ctx context.Context, s KVServicer) error {
		_, err := s.DeleteEntity(ctx, &DeleteEntityRequest{
			EntityType: "user",
			ID:         "1",
		})
		return err
	},
}

// readVerbs are the KVServicer methods that store nothing, so they have no
// outcome to classify. Named explicitly so the exhaustiveness check below can
// tell "read" from "forgotten".
var readVerbs = map[string]struct{}{
	"Get": {}, "GetStale": {}, "GetWithConsistency": {},
	"List": {}, "ListAll": {}, "ListStale": {}, "ListWithConsistency": {},
	"Scan": {}, "ScanStale": {}, "ScanWithConsistency": {},
	"ExistsWithConsistency": {},
	"GetEntity":             {}, "ListEntities": {},
}

// Every method on KVServicer is accounted for as a write under test or a read
// with nothing to classify.
//
// Without this, the classification tests below cover whatever the table
// happened to list on the day it was written, and the next verb added to the
// interface silently escapes them.
func TestKVServicer_EveryMethodIsClassifiedAsWriteOrRead(t *testing.T) {
	iface := reflect.TypeOf((*KVServicer)(nil)).Elem()

	var unaccounted []string
	for i := 0; i < iface.NumMethod(); i++ {
		name := iface.Method(i).Name
		if _, ok := writeVerbs[name]; ok {
			continue
		}
		if _, ok := readVerbs[name]; ok {
			continue
		}
		unaccounted = append(unaccounted, name)
	}
	sort.Strings(unaccounted)

	if len(unaccounted) > 0 {
		t.Fatalf("KVServicer methods absent from both tables: %v\n"+
			"a write verb missing from writeVerbs is a write path whose failure "+
			"nobody classifies; add it there, or to readVerbs if it stores nothing",
			unaccounted)
	}
}

// proposeFailureHost fails every proposal with err.
//
// BOTH propose entry points are failed. The mode-carrying verbs go through
// ProposeWithMode rather than SyncPropose, and a fake that stubbed only the
// latter let them return success from a host that was supposed to be failing —
// which the verb table caught, and a hand-picked list of verbs would not have.
func proposeFailureHost(err error) *mockHost {
	return &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, err
		},
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, err
		},
	}
}

// A proposal that failed for an unclassifiable reason leaves the outcome
// UNKNOWN, on every write verb.
//
// This is the case the marker exists for. SyncPropose can commit the entry and
// then fail to deliver its response, so the value is durable and the caller was
// told only that something went wrong. A caller that reconciles state must not
// act as though the write did not happen.
func TestKVService_ProposeFailureLeavesEveryWriteUnknown(t *testing.T) {
	transportFailure := errors.New("connection reset by peer")

	for name, call := range writeVerbs {
		t.Run(name, func(t *testing.T) {
			svc := NewKVService(proposeFailureHost(transportFailure), 1)

			err := call(context.Background(), svc)
			if err == nil {
				t.Fatal("a failing proposal returned no error")
			}
			if !errors.Is(err, ErrOutcomeUnknown) {
				t.Errorf("error %v (%T) is not marked ErrOutcomeUnknown", err, err)
			}
			if errors.Is(err, ErrNotStored) {
				t.Errorf("error %v (%T) claims the write is PROVEN not stored; "+
					"a proposal that failed in transit may have committed", err, err)
			}
		})
	}
}

// A proposal refused because this node is not the leader is PROVEN not stored,
// on every write verb.
//
// The entry was never accepted into the log. A leader elsewhere may later write
// something, but not this call's bytes.
func TestKVService_NotLeaderProvesEveryWriteNotStored(t *testing.T) {
	notLeader := &quicraft.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "127.0.0.1:9200"}

	for name, call := range writeVerbs {
		t.Run(name, func(t *testing.T) {
			svc := NewKVService(proposeFailureHost(notLeader), 1)

			err := call(context.Background(), svc)
			if err == nil {
				t.Fatal("a refused proposal returned no error")
			}
			if !errors.Is(err, ErrNotStored) {
				t.Errorf("error %v (%T) is not marked ErrNotStored, so a caller "+
					"must keep accounting for a write that was never accepted", err, err)
			}
			if errors.Is(err, ErrOutcomeUnknown) {
				t.Errorf("error %v (%T) is marked BOTH proven-not-stored and unknown", err, err)
			}
		})
	}
}

// A state machine that applied the entry and refused the command stored
// nothing, and says so.
//
// The entry reached consensus, so this is not a proposal failure at all: the
// command was applied and the state machine encoded its refusal into the
// result — a subsystem it was not built with, an invalid lease TTL, a
// unique-index violation. Nothing was written.
func TestKVService_StateMachineRefusalProvesNotStored(t *testing.T) {
	refusing := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Data: protocol.EncodeAppError("timeseries subsystem not configured")}, nil
		},
	}
	svc := NewKVService(refusing, 1)

	err := svc.Put(context.Background(), "k", []byte("v"))
	if err == nil {
		t.Fatal("a refused command returned no error")
	}
	if !errors.Is(err, ErrNotStored) {
		t.Errorf("error %v (%T) is not marked ErrNotStored", err, err)
	}
}

// A request that could not be encoded was never proposed.
func TestKVService_EncodeFailureProvesNotStored(t *testing.T) {
	err := (&EncodeRequestError{Err: errors.New("key too long")})
	if !errors.Is(error(err), ErrNotStored) {
		t.Error("EncodeRequestError is not marked ErrNotStored")
	}
}

// An entity rejected on validation was never proposed.
func TestKVService_EntityValidationProvesNotStored(t *testing.T) {
	svc := NewKVService(&mockHost{}, 1)

	// Every field the validator rejects, so the marker cannot be satisfied by
	// whichever one happens to be checked first.
	cases := map[string]*PutEntityRequest{
		"empty type": {ID: "1", Data: []byte("v")},
		"empty id":   {EntityType: "user", Data: []byte("v")},
		"empty data": {EntityType: "user", ID: "1"},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.PutEntity(context.Background(), req)
			if err == nil {
				t.Fatal("an invalid entity was accepted")
			}
			if !errors.Is(err, ErrNotStored) {
				t.Errorf("error %v (%T) is not marked ErrNotStored, so a rejected "+
					"request keeps its caller's accounting forever", err, err)
			}
		})
	}
}

// nonAtomicBackend is a Backend without the AtomicBackend capability.
type nonAtomicBackend struct {
	database.Backend
}

// A capability the backend does not have is refused before any write, and the
// refusal still matches the sentinel callers already test for.
func TestBackendKVService_CapabilityRefusalProvesNotStored(t *testing.T) {
	svc := NewBackendKVService(&nonAtomicBackend{})

	calls := map[string]func() error{
		"PutIfAbsent": func() error {
			_, err := svc.PutIfAbsent(context.Background(), "k", []byte("v"))
			return err
		},
		"CompareAndDelete": func() error {
			_, err := svc.CompareAndDelete(context.Background(), "k", []byte("old"))
			return err
		},
		"CompareAndSwap": func() error {
			_, err := svc.CompareAndSwap(context.Background(), "k", []byte("old"), []byte("new"))
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if !errors.Is(err, ErrNotStored) {
				t.Errorf("error %v is not marked ErrNotStored", err)
			}
			// The marker is additive: code that matched on the sentinel before
			// the marker existed must keep working.
			if !errors.Is(err, database.ErrAtomicWriteUnsupported) {
				t.Errorf("error %v no longer matches database.ErrAtomicWriteUnsupported", err)
			}
		})
	}
}

// storageFailureBackend fails writes with an error from the storage engine,
// which no layer here can classify.
type storageFailureBackend struct {
	database.Backend
	err error
}

func (b *storageFailureBackend) Put(_ context.Context, _, _ []byte) error {
	return b.err
}

// An error forwarded from the storage engine is UNKNOWN, because it carries no
// marker.
//
// This is the property that makes the default safe, and it is why ErrNotStored
// is the marked case rather than ErrOutcomeUnknown. A write that failed
// mid-fsync may or may not be durable, and no layer here can tell; if absence
// of a marker meant "proven not stored", every unclassifiable third-party error
// would silently become the fail-open answer (go-qrdb#90, #100).
func TestBackendKVService_StorageFailureIsUnknown(t *testing.T) {
	svc := NewBackendKVService(&storageFailureBackend{err: errors.New("pebble: write batch failed")})

	err := svc.Put(context.Background(), "k", []byte("v"))
	if err == nil {
		t.Fatal("a failing backend write returned no error")
	}
	if errors.Is(err, ErrNotStored) {
		t.Errorf("error %v claims the write is PROVEN not stored; no layer here "+
			"knows that about a storage engine failure", err)
	}
}

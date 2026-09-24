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

//go:build !integration

package node

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// nodeAccessors is every exported method on *Node that takes no argument —
// i.e. every accessor the concurrency contract in Node's doc comment covers.
// It is derived from the type rather than hand-listed on purpose: a hand-list
// silently stops covering the next accessor someone adds, which is exactly how
// IsSealed and BarrierRegistry were missed the first time round.
func nodeAccessors() []reflect.Method {
	var out []reflect.Method
	t := reflect.TypeOf((*Node)(nil))
	for i := range t.NumMethod() {
		m := t.Method(i)
		// NumIn()==1 is the receiver alone. Unseal, EnableEncryption and Close
		// take arguments or mutate, and are drivers in these tests, not probes.
		if m.Type.NumIn() == 1 && m.Name != "Close" {
			out = append(out, m)
		}
	}
	return out
}

// pollNodeAccessors calls every no-argument exported accessor and reports how
// many returned something non-nil. It never dereferences a returned handle:
// doing so would be a use-after-Close, a different contract from the
// publication one under test. The count exists only so the calls are not
// optimised away.
func pollNodeAccessors(n *Node, accessors []reflect.Method) int {
	rv := reflect.ValueOf(n)
	var seen int
	for _, m := range accessors {
		for _, out := range m.Func.Call([]reflect.Value{rv}) {
			switch out.Kind() {
			case reflect.Bool:
				if out.Bool() {
					seen++
				}
			case reflect.Pointer, reflect.Interface:
				if !out.IsNil() {
					seen++
				}
			default:
				seen++
			}
		}
	}
	return seen
}

// assertAllAccessorsNil checks that every no-argument exported accessor reads
// nil (or false) after Close. That holds only for a caller that did NOT overlap
// Close with Unseal or a TENANT-scope EnableEncryption — Node has no general
// lifecycle state, so those two interleavings are undefined (go-qrdb#226) and
// no test here creates one. A SYSTEM-scope EnableEncryption is excluded from
// that list: Close now blocks on migrateMu until any in-flight system
// migration finishes (see Close's doc comment), so overlapping the two no
// longer produces a race to assert around here. This helper names the
// offender, so a handle someone forgets to retract fails here rather than
// becoming a stale handle in a consumer.
func assertAllAccessorsNil(t *testing.T, n *Node, accessors []reflect.Method) {
	t.Helper()
	rv := reflect.ValueOf(n)
	for _, m := range accessors {
		for _, out := range m.Func.Call([]reflect.Value{rv}) {
			switch out.Kind() {
			case reflect.Bool:
				assert.False(t, out.Bool(), "%s() must be false after Close", m.Name)
			case reflect.Pointer, reflect.Interface:
				assert.True(t, out.IsNil(), "%s() must be nil after Close: a non-nil handle onto a stopped service graph is exactly the stale handle this contract forbids", m.Name)
			default:
				t.Fatalf("%s() returns %s, which this assertion does not know how to check; extend it rather than skipping the accessor", m.Name, out.Kind())
			}
		}
	}
}

// TestNode_AccessorsRaceFreeDuringUnseal is the regression test for
// go-qrdb#176. A node built with a passphrase barrier and no passphrase is
// handed back to the embedder BEFORE its data plane exists, and every
// data-plane accessor documents a nil return for exactly that window — an
// invitation to poll them while another goroutine drives Unseal. Two
// independent go-quicbaas runs measured the resulting report,
//
//	Read at Client() <- Previous write at wireClientAndServer()
//
// the second reproduced at a clean consumer base, which is what established
// the defect as go-qrdb's rather than the consumer's.
//
// The readers only LOAD each handle; they never call through it. That keeps
// the test aimed at publication and away from use-after-Close, which is a
// different contract.
//
// This test is only meaningful under -race: without the detector an
// unsynchronised word read is very unlikely to observe a torn or stale value
// on amd64/arm64, so a green run of `go test` alone proves nothing here.
// `make test-sdk` runs the sdk module with -race.
func TestNode_AccessorsRaceFreeDuringUnseal(t *testing.T) {
	const passphrase = "unseal-race-passphrase"

	dataDir := t.TempDir()
	cfg := sealedStandaloneConfig(t, dataDir)

	// First boot initializes the barrier so the second boot can come back
	// sealed rather than uninitialized.
	n1, err := NewStandaloneNode(context.Background(), cfg, nil,
		WithSealPassphrase([]byte(passphrase)))
	require.NoError(t, err)
	require.NoError(t, n1.Close())

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.True(t, n.IsSealed(), "second boot must return a sealed node")

	accessors := nodeAccessors()
	require.NotEmpty(t, accessors, "reflection must find the exported accessors")

	const readers = 8
	var (
		ready sync.WaitGroup
		done  sync.WaitGroup
	)
	stop := make(chan struct{})
	ready.Add(readers)
	done.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer done.Done()
			ready.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = pollNodeAccessors(n, accessors)
				}
			}
		}()
	}
	// Every reader is inside its loop before the boot publishes anything, so
	// the Unseal below overlaps the reads rather than following them.
	ready.Wait()

	require.NoError(t, n.Unseal(context.Background(), seal.MustCredentials([]byte(passphrase))))

	close(stop)
	done.Wait()

	assert.False(t, n.IsSealed(), "node must be unsealed after Unseal")
	assert.NotNil(t, n.Client(), "client must be published by the completed boot")
	assert.NotNil(t, n.Server(), "server must be published by the completed boot")
	assert.NotNil(t, n.Services(), "services must be published by the completed boot")

	require.NoError(t, n.Close())
}

// TestNode_AccessorsRaceFreeDuringClose covers the retraction half. Close and
// teardownDataPlane (the in-process encryption-migration recycle) clear the very
// handles the boot published, on a node the embedder already holds, so they are
// a second writer against the same readers.
//
// Scope note: this drives accessor READS against Close. It does not drive a
// BOOT against Close — Close racing Unseal is an ordering gap Node has no
// lifecycle state to close, tracked as go-qrdb#226 and deliberately out of
// scope for #176.
func TestNode_AccessorsRaceFreeDuringClose(t *testing.T) {
	dataDir := t.TempDir()
	cfg := sealedStandaloneConfig(t, dataDir)

	n, err := NewStandaloneNode(context.Background(), cfg, nil,
		WithSealPassphrase([]byte("close-race-passphrase")))
	require.NoError(t, err)
	require.False(t, n.IsSealed())

	accessors := nodeAccessors()
	require.NotEmpty(t, accessors, "reflection must find the exported accessors")

	const readers = 8
	var (
		ready sync.WaitGroup
		done  sync.WaitGroup
	)
	stop := make(chan struct{})
	ready.Add(readers)
	done.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer done.Done()
			ready.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = pollNodeAccessors(n, accessors)
				}
			}
		}()
	}
	ready.Wait()

	require.NoError(t, n.Close())

	close(stop)
	done.Wait()

	// No boot overlaps this Close, so the retraction guarantee applies: nil from
	// EVERY accessor, not just the obvious three.
	assertAllAccessorsNil(t, n, accessors)
}

// TestNode_CloseRetractsEveryHandle_Configured is the retraction test with the
// optional handles actually populated. The plain close-race node configures
// neither ABAC nor a factor registry, so abacDecorator/authFactorSvc are nil
// before Close and a post-Close nil assertion on them proves nothing. Here they
// are non-nil first, so the assertion has something to retract.
func TestNode_CloseRetractsEveryHandle_Configured(t *testing.T) {
	dataDir := t.TempDir()
	cfg := sealedStandaloneConfig(t, dataDir)

	n, err := NewStandaloneNode(context.Background(), cfg, nil,
		WithSealPassphrase([]byte("retract-passphrase")),
		WithABACLiveReload(),
	)
	require.NoError(t, err)
	require.False(t, n.IsSealed())

	// Preconditions: without these the post-Close nil checks are vacuous.
	require.NotNil(t, n.ABACPolicyReloader(), "WithABACLiveReload must publish a reload seam")
	require.NotNil(t, n.UserAuthFactorService(), "a node with a User service must publish the login factor service")
	require.NotNil(t, n.BarrierRegistry(), "a sealed-capable node must publish a barrier registry")
	require.NotNil(t, n.Client())
	require.NotNil(t, n.Server())
	require.NotNil(t, n.Services())
	// encMigrator has no no-argument accessor (EnableEncryption takes a scope
	// and options), so the reflective sweep cannot reach it. Check it directly
	// rather than leave the one non-enumerable handle unverified.
	require.NotNil(t, n.encMigrator.Load(), "wireClientAndServer must publish the scoped encryption router")

	require.NoError(t, n.Close())

	assertAllAccessorsNil(t, n, nodeAccessors())
	assert.Nil(t, n.encMigrator.Load(), "Close must retract the scoped encryption router")

	// And the retraction must degrade to a typed refusal, not a nil panic.
	err = n.EnableEncryption(context.Background(), service.BarrierScope{Kind: service.ScopeSystem}, seal.MigrationOptions{})
	var notSupported *MigrationNotSupportedError
	require.ErrorAs(t, err, &notSupported, "EnableEncryption after Close must return a typed refusal")
}

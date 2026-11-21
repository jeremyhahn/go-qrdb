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
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
)

// closeRaceAuditSink is an audit sink whose teardown is an unsynchronized
// write the way an embedder's is (go-xkms closes its audit sink and its
// shared TPM right after node.Close): every Emit reads torn, and the test
// sets torn without a lock once Close has returned. A goroutine the node
// constructor started that still emits after Close is a data race the race
// detector reports against that write.
type closeRaceAuditSink struct {
	*audit.RingBuffer
	torn bool
}

func (s *closeRaceAuditSink) Emit(ctx context.Context, ev audit.AuditEvent) error {
	if s.torn {
		return nil
	}
	return s.RingBuffer.Emit(ctx, ev)
}

// qrdbGoroutines returns the stacks of the live goroutines that run or were
// created by this module's code, keyed by goroutine header ("goroutine N").
func qrdbGoroutines() map[string]string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	out := make(map[string]string)
	for _, g := range strings.Split(string(buf), "\n\n") {
		if !strings.Contains(g, "github.com/jeremyhahn/go-qrdb/") {
			continue
		}
		head, _, _ := strings.Cut(g, " [")
		out[head] = g
	}
	return out
}

// TestNodeCloseJoinsConstructorGoroutines pins go-qrdb#1665: Node.Close
// returns only after every goroutine the node constructor started has
// exited, so an embedder may tear down what it passed in (the audit sink)
// straight after Close without racing one of them.
//
// Both constructors are covered: an embedder (go-xkms pkg/embeddednode)
// builds either one.
func TestNodeCloseJoinsConstructorGoroutines(t *testing.T) {
	t.Run("standalone", func(t *testing.T) {
		assertCloseJoinsConstructorGoroutines(t, func(opts []server.ServerOption) (*Node, error) {
			return NewStandaloneNode(context.Background(), newMemoryNodeConfig(t), opts)
		})
	})
	t.Run("clustered", func(t *testing.T) {
		assertCloseJoinsConstructorGoroutines(t, func(opts []server.ServerOption) (*Node, error) {
			return NewClusteredNode(context.Background(), newRaftIsolationConfig(t), raftMembers(), opts, WithNoSyncWrites())
		})
	})
}

func assertCloseJoinsConstructorGoroutines(t *testing.T, build func([]server.ServerOption) (*Node, error)) {
	t.Helper()
	before := qrdbGoroutines()

	sink := &closeRaceAuditSink{RingBuffer: audit.NewRingBuffer(64)}
	n, err := build([]server.ServerOption{server.WithAuditLogger(sink)})
	require.NoError(t, err)
	require.Greater(t, len(qrdbGoroutines()), len(before),
		"the open node runs goroutines of its own; without them this test proves nothing")
	require.NoError(t, n.Close())

	// The embedder's teardown: an unsynchronized write and the sink's Close.
	sink.torn = true
	require.NoError(t, sink.RingBuffer.Close())

	// Close already joined them, so they are gone at once; the poll only
	// absorbs a goroutine between its last statement and its exit.
	var leaked []string
	deadline := time.Now().Add(2 * time.Second)
	for {
		leaked = leaked[:0]
		for head, stack := range qrdbGoroutines() {
			if _, ok := before[head]; !ok {
				leaked = append(leaked, stack)
			}
		}
		if len(leaked) == 0 || time.Now().After(deadline) {
			break
		}
		runtime.Gosched()
	}
	require.Empty(t, leaked, "goroutines started by the node constructor outlive Close")
}

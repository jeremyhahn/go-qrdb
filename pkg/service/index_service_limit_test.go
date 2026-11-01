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
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// indexRegistryHost is a shard whose apply path decides index.max_indexes the
// way the engines do: against the index set each create applies over, one
// command at a time in log order (mu stands in for the log). Its proposals
// can be held at a barrier so creates from several nodes are all in flight
// before the first one applies.
type indexRegistryHost struct {
	mockHost
	mu      sync.Mutex
	names   []string
	reads   atomic.Int32
	caps    []uint32
	barrier *sync.WaitGroup
}

func newIndexRegistryHost(names ...string) *indexRegistryHost {
	h := &indexRegistryHost{names: names}
	h.syncReadFn = func(context.Context, uint64, interface{}) (interface{}, error) {
		h.reads.Add(1)
		h.mu.Lock()
		defer h.mu.Unlock()
		return slices.Clone(h.names), nil
	}
	h.syncProposeFn = func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
		if h.barrier != nil {
			h.barrier.Done()
			h.barrier.Wait()
		}
		name, _, opts, err := protocol.DecodeIndexCreateOpts(cmd[1:])
		if err != nil {
			return sm.Result{}, err
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		h.caps = append(h.caps, opts.MaxIndexes)
		exists := slices.Contains(h.names, name)
		if index.CreateExceedsCap(exists, len(h.names), opts.MaxIndexes) {
			return sm.Result{Data: protocol.EncodeLimitRefusal(protocol.LimitIndexes, opts.MaxIndexes)}, nil
		}
		if !exists {
			h.names = append(h.names, name)
		}
		return sm.Result{Value: 1}, nil
	}
	return h
}

func (h *indexRegistryHost) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.names)
}

func (h *indexRegistryHost) proposedCaps() []uint32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.caps)
}

func TestWithMaxIndexes_SetsTheCap(t *testing.T) {
	svc := NewIndexService(newIndexRegistryHost(), 1, WithMaxIndexes(3)).(*IndexService)
	if svc.maxIndexes != 3 {
		t.Fatalf("maxIndexes = %d, want 3", svc.maxIndexes)
	}
}

func TestWithMaxIndexes_NoOptionIsUnlimited(t *testing.T) {
	svc := NewIndexService(newIndexRegistryHost(), 1).(*IndexService)
	if svc.maxIndexes != 0 {
		t.Fatalf("maxIndexes = %d, want 0 (unlimited)", svc.maxIndexes)
	}
}

func TestIndexService_CreateIndex_CarriesTheCapInTheCommand(t *testing.T) {
	host := newIndexRegistryHost()
	svc := NewIndexService(host, 1, WithMaxIndexes(7))
	if err := svc.CreateIndex(context.Background(), "a", "f", IndexConfig{}); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}
	if got := host.proposedCaps(); !slices.Equal(got, []uint32{7}) {
		t.Fatalf("proposed caps = %v, want [7]", got)
	}
}

func TestIndexService_CreateIndex_RefusesPastMaxIndexes(t *testing.T) {
	host := newIndexRegistryHost("a", "b")
	svc := NewIndexService(host, 1, WithMaxIndexes(2))
	err := svc.CreateIndex(context.Background(), "c", "f", IndexConfig{})
	var le *IndexLimitError
	if !errors.As(err, &le) || le.Max != 2 {
		t.Fatalf("CreateIndex past the cap = %v, want *IndexLimitError{Max: 2}", err)
	}
	if host.count() != 2 {
		t.Fatalf("indexes = %d, want 2", host.count())
	}
}

func TestIndexService_CreateIndex_AdmitsUnderMaxIndexes(t *testing.T) {
	host := newIndexRegistryHost("a")
	svc := NewIndexService(host, 1, WithMaxIndexes(2))
	if err := svc.CreateIndex(context.Background(), "b", "f", IndexConfig{}); err != nil {
		t.Fatalf("CreateIndex under the cap: %v", err)
	}
	if host.count() != 2 {
		t.Fatalf("indexes = %d, want 2", host.count())
	}
}

func TestIndexService_CreateIndex_ExistingNameAtTheCapIsAdmitted(t *testing.T) {
	host := newIndexRegistryHost("a", "b")
	svc := NewIndexService(host, 1, WithMaxIndexes(2))
	if err := svc.CreateIndex(context.Background(), "a", "f", IndexConfig{}); err != nil {
		t.Fatalf("CreateIndex naming an existing index at the cap: %v", err)
	}
}

// The cap is the apply path's to decide: the service reads nothing ahead of
// the proposal, capped or not.
func TestIndexService_CreateIndex_ReadsNothingBeforeProposing(t *testing.T) {
	for _, maxIndexes := range []int{0, 1} {
		host := newIndexRegistryHost()
		svc := NewIndexService(host, 1, WithMaxIndexes(maxIndexes))
		if err := svc.CreateIndex(context.Background(), "a", "f", IndexConfig{}); err != nil {
			t.Fatalf("max %d: CreateIndex: %v", maxIndexes, err)
		}
		if n := host.reads.Load(); n != 0 {
			t.Fatalf("max %d: CreateIndex read the index list %d times, want 0", maxIndexes, n)
		}
	}
}

// Two nodes serving one shard each propose a create for its last slot, and
// both proposals are in flight before either applies -- the interleaving a
// check in the service ahead of the proposal admitted twice (go-qrdb#1088).
// The apply path admits exactly one.
func TestIndexService_CreateIndex_CreatesThroughTwoNodesTakeTheLastSlotOnce(t *testing.T) {
	host := newIndexRegistryHost()
	host.barrier = &sync.WaitGroup{}
	nodes := []IndexServicer{
		NewIndexService(host, 1, WithMaxIndexes(1)),
		NewIndexService(host, 1, WithMaxIndexes(1)),
	}
	host.barrier.Add(len(nodes))
	errs := make(chan error, len(nodes))
	var wg sync.WaitGroup
	for i, node := range nodes {
		wg.Add(1)
		go func(i int, node IndexServicer) {
			defer wg.Done()
			errs <- node.CreateIndex(context.Background(), []string{"x", "y"}[i], "f", IndexConfig{})
		}(i, node)
	}
	wg.Wait()
	close(errs)
	var admitted, refused int
	for err := range errs {
		var le *IndexLimitError
		switch {
		case err == nil:
			admitted++
		case errors.As(err, &le):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if admitted != 1 || refused != 1 || host.count() != 1 {
		t.Fatalf("admitted %d, refused %d, indexes %d; want 1, 1, 1", admitted, refused, host.count())
	}
}

func TestIndexLimitError_NamesTheCap(t *testing.T) {
	if got, want := (&IndexLimitError{Max: 64}).Error(), "service: index limit reached: max_indexes 64"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestCommandCap(t *testing.T) {
	for n, want := range map[int]uint32{-1: 0, 0: 0, 5: 5, 1 << 40: 1<<32 - 1} {
		if got := commandCap(n); got != want {
			t.Fatalf("commandCap(%d) = %d, want %d", n, got, want)
		}
	}
}

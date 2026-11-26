package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

var errRaftReadFailed = errors.New("raft read: no leader")

// failingReadHost is a Raft host whose reads fail with readErr, as a
// group with no leader or a sealed Pebble state machine answers.
type failingReadHost struct {
	service.Host
	readErr error
}

func (h *failingReadHost) SyncRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	if h.readErr != nil {
		return nil, h.readErr
	}
	return h.Host.SyncRead(ctx, shardID, query)
}

func newFailingReadVectorService(t *testing.T) (service.VectorService, *failingReadHost) {
	t.Helper()
	eng, err := engine.NewMVCCEngine(memory.NewStorage())
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := &failingReadHost{Host: backend.NewLocalBackend(eng)}
	svc, err := service.NewVectorService(nil, hostVecLogger(t), service.WithVectorApplyHost(host, shard.ShardKV))
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	t.Cleanup(func() { closeVectorService(svc) })
	return svc, host
}

// A failed Raft read is the caller's error, never an empty successful
// search.
func TestVectorService_SearchSimilarReportsFailedRead(t *testing.T) {
	svc, host := newFailingReadVectorService(t)
	host.readErr = errRaftReadFailed
	got, err := searchNS(svc)
	if !errors.Is(err, errRaftReadFailed) {
		t.Fatalf("SearchSimilar over a failing read = %+v, %v; want the read error", got, err)
	}
	if got != nil {
		t.Fatalf("a failed read returned a response %+v", got)
	}
}

// A Pebble binding store under a sealed barrier refuses the read with its
// own sealed error, which SearchSimilar reports as *BarrierSealedError.
func TestVectorService_SearchSimilarVectorSealedPebble(t *testing.T) {
	for name, readErr := range map[string]error{
		"SealedError":      &pebble.SealedError{Err: pebble.ErrBarrierSealed},
		"ErrBarrierSealed": pebble.ErrBarrierSealed,
	} {
		t.Run(name, func(t *testing.T) {
			svc, host := newFailingReadVectorService(t)
			host.readErr = readErr
			got, err := searchNS(svc)
			var sealed *service.BarrierSealedError
			if !errors.As(err, &sealed) {
				t.Fatalf("SearchSimilar on a sealed Pebble store = %+v, %v; want *BarrierSealedError", got, err)
			}
		})
	}
}

// A namespace that does not exist still answers with no results.
func TestVectorService_SearchSimilarMissingNamespaceIsEmpty(t *testing.T) {
	svc, _ := newFailingReadVectorService(t)
	got, err := searchNS(svc)
	if err != nil {
		t.Fatalf("SearchSimilar on a missing namespace: %v", err)
	}
	if got == nil || len(got.Results) != 0 {
		t.Fatalf("SearchSimilar on a missing namespace = %+v; want empty results", got)
	}
}

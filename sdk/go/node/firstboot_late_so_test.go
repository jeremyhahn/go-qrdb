package node

import (
	"context"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
)

// lateSOStore models a pending system SO record that lands after first boot
// read there was none: the first read of its key finds nothing, later reads
// find the record. Every write is recorded.
type lateSOStore struct {
	mu     sync.Mutex
	soRead int
	writes map[string][]byte
}

var _ firstboot.PendingAdminCertStore = (*lateSOStore)(nil)

func (s *lateSOStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.writes[key]; ok {
		return v, nil
	}
	if key != firstboot.PendingSystemSOKey {
		return nil, nil
	}
	s.soRead++
	if s.soRead == 1 {
		return nil, nil
	}
	return []byte(`{"Username":"officer"}`), nil
}

func (s *lateSOStore) put(key string, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writes == nil {
		s.writes = map[string][]byte{}
	}
	s.writes[key] = value
}

func (s *lateSOStore) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	s.put(key, value)
	return true, nil
}

func (s *lateSOStore) Put(_ context.Context, key string, value []byte) error {
	s.put(key, value)
	return nil
}

func (s *lateSOStore) Delete(context.Context, string) error { return nil }

// TestNodeFirstBootWithCeremony_LateSystemSORecordStaysPending: a system SO
// record written between first boot's read and its cleanup names an SO this
// run never enrolled, so the cleanup must leave it pending for the next
// first boot instead of marking it consumed.
func TestNodeFirstBootWithCeremony_LateSystemSORecordStaysPending(t *testing.T) {
	users, roles := realStores(t)
	store := &lateSOStore{}
	n := &Node{}
	n.bindingPolicy.Store(int32(auth.MTLSBindingEnrolled))

	if _, err := n.FirstBootWithCeremony(context.Background(), store, nil, 0, fbDeps(users, roles)); err != nil {
		t.Fatalf("FirstBootWithCeremony = %v", err)
	}
	if store.soRead < 2 {
		t.Fatalf("the cleanup never re-read the system SO record (reads = %d)", store.soRead)
	}
	if _, consumed := store.writes[firstboot.PendingSystemSOKey]; consumed {
		t.Fatal("the cleanup consumed a system SO record for an SO this run never enrolled")
	}
	if firstboot.SystemSOConsumed(context.Background(), store) {
		t.Fatal("the late system SO record reads as consumed")
	}
}

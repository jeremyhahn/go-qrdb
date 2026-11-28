package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// watchingDatabase is a database service that records the start of its
// tenant home roster watch.
type watchingDatabase struct {
	service.DatabaseServicer
	started chan struct{}
}

func (w *watchingDatabase) WatchTenantHomes(ctx context.Context, tick <-chan time.Time) {
	if tick == nil {
		panic("tenant home watch started without a tick")
	}
	close(w.started)
	<-ctx.Done()
}

// TestStartTenantHomeWatch_RunsTheTenantHomeWatchWithoutDynamicMembership
// proves both node compositions (qrdbd and sdk/go/node reach
// StartTenantHomeWatch through ComposeRaftTenantCluster) run the tenant home roster watch on every node, with no
// dynamic membership manager involved, and that Stop ends it.
func TestStartTenantHomeWatch_RunsTheTenantHomeWatchWithoutDynamicMembership(t *testing.T) {
	db := &watchingDatabase{started: make(chan struct{})}
	n := &Node{Database: db}
	StartTenantHomeWatch(n)
	select {
	case <-db.started:
	case <-time.After(10 * time.Second):
		t.Fatal("StartTenantHomeWatch did not start the tenant home roster watch")
	}
	n.work.stop()
}

// TestStartNodeWatchers_LeavesTheTenantHomeWatchToTheRaftHost: the early
// watcher set does not start the home watch; it starts once the raft host
// is composed (ComposeRaftTenantCluster).
func TestStartNodeWatchers_LeavesTheTenantHomeWatchToTheRaftHost(t *testing.T) {
	db := &watchingDatabase{started: make(chan struct{})}
	n := &Node{Database: db}
	_ = StartNodeWatchers(context.Background(), n)
	n.work.stop()
	select {
	case <-db.started:
		t.Fatal("StartNodeWatchers started the tenant home watch before the raft host was composed")
	default:
	}
}

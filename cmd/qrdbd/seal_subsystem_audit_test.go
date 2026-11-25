package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestInitSealSubsystem_BootMigrationIsAudited: the boot-time migration of
// node-local credentials runs when BuildServices binds the replicated store,
// before runStart attaches anything else to the credential service. The
// subsystem's credential service must already carry the compliance trail
// then, or the migration's events are lost.
func TestInitSealSubsystem_BootMigrationIsAudited(t *testing.T) {
	ctx := context.Background()
	trail := audit.NewRingBuffer(64)
	f := &startFlags{
		dataDir:      t.TempDir(),
		deploymentID: 1,
		sealConfig:   qrdbconfig.SealConfig{Strategy: string(seal.StrategyNone), RootKeyPath: "seal-root"},
	}
	sub, err := initSealSubsystem(f, ctx, trail)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Close() })
	require.NoError(t, sub.SealBarrier.Initialize(ctx, seal.Credentials{}))
	require.NoError(t, sub.SealBarrier.Put(ctx, "credentials/db-pass", []byte("legacy-secret")))

	replicated, err := credentials.NewKVStore(kvstoretest.NewGuardedMemoryStore())
	require.NoError(t, err)
	require.NoError(t, sub.CredCoreSvc.BindStore(ctx, replicated))

	var migrations int
	for _, ev := range trail.Snapshot() {
		if strings.Contains(ev.Action, "migrate") {
			migrations++
		}
	}
	require.Positive(t, migrations, "the boot migration reached the compliance trail")
}

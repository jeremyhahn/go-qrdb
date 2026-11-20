//go:build !integration

package node

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// TestClusteredNode_CredentialsLiveOnTheRaftDataPlane builds an encrypted
// node through NewClusteredNode: its credential records must go to the
// replicated Raft data plane, not the node-local seal barrier, or a peer
// never sees a system credential written here and migrated copies rotate
// independently on every node.
func TestClusteredNode_CredentialsLiveOnTheRaftDataPlane(t *testing.T) {
	ctx := context.Background()
	n := bootClusteredWithDefaults(t, qrdbconfig.DatabaseDefaults{}, true)
	barrier := n.barrier.Load()
	require.NotNil(t, barrier)
	require.False(t, barrier.IsSealed(), "the test needs an unsealed barrier")
	require.NotNil(t, n.credCoreSvc)

	require.NoError(t, n.credCoreSvc.StoreCredential(ctx, "db-password", []byte("v1-secret")))

	keys, err := barrier.List(ctx, "")
	require.NoError(t, err)
	for _, k := range keys {
		require.False(t, strings.Contains(k, "db-password"),
			"credential record %q is in the node-local seal barrier, want it on the Raft data plane", k)
	}
	buf, err := n.credCoreSvc.RetrieveCredentialBuffer(ctx, "db-password")
	require.NoError(t, err)
	buf.Destroy()
}

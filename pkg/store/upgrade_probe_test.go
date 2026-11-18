//go:build qrdb_upgrade_probe

package store

import (
	"bytes"
	"context"
	"testing"

	sm "github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// TestUpgradeProbe_Applies proves a qrdb_upgrade_probe build (the matrix's
// synthetic N+1) applies protocol.OpUpgradeProbe as an MVCC put.
func TestUpgradeProbe_Applies(t *testing.T) {
	s := openExtendedStore(t)

	cmd := mustEncode(protocol.EncodeMVCCPut([]byte("upgrade-probe/k"), []byte("probe-val"), 0))
	cmd[0] = protocol.OpUpgradeProbe
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})
	if results[0].Value != uint64(len("probe-val")) {
		t.Fatalf("result = %d, want %d", results[0].Value, len("probe-val"))
	}

	val, err := s.Lookup(context.Background(), []byte("upgrade-probe/k"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("probe-val")) {
		t.Fatalf("value = %q, want probe-val", val)
	}
}

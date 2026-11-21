//go:build !qrdb_upgrade_probe

package store

import (
	"context"
	"testing"

	sm "github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// TestUpgradeProbe_RefusedWithoutTag proves a binary without the
// qrdb_upgrade_probe tag (the matrix's "N") refuses protocol.OpUpgradeProbe
// as an unknown opcode -- an absent capability, encoded into the result so
// consensus does not stall -- and stores nothing: the reason the command may
// be proposed only once every member supports featuregate.UpgradeProbe.
func TestUpgradeProbe_RefusedWithoutTag(t *testing.T) {
	s := openExtendedStore(t)

	cmd := mustEncode(protocol.EncodeMVCCPut([]byte("upgrade-probe/k"), []byte("v"), 0))
	cmd[0] = protocol.OpUpgradeProbe
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	msg, ok := protocol.DecodeAppError(results[0].Data)
	want := (&protocol.UnknownOpError{Op: protocol.OpUpgradeProbe}).Error()
	if !ok || msg != want {
		t.Fatalf("result = %q (app error %v), want %q", msg, ok, want)
	}
	if _, lookupErr := s.Lookup(context.Background(), []byte("upgrade-probe/k")); lookupErr == nil {
		t.Fatal("refused probe command stored its key")
	}
}

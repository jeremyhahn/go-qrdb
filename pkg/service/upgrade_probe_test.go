//go:build qrdb_upgrade_probe

package service

import (
	"context"
	"testing"

	sm "github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// upgradeProbeHost records every proposed command and reports
// featuregate.UpgradeProbe as enabled when enabled is set.
type upgradeProbeHost struct {
	mockHost
	enabled  bool
	asked    []feature.Feature
	proposed [][]byte
}

func (h *upgradeProbeHost) FeatureEnabled(_ uint64, f feature.Feature) bool {
	h.asked = append(h.asked, f)
	return h.enabled && f == featuregate.UpgradeProbe
}

func newUpgradeProbeHost(enabled bool) *upgradeProbeHost {
	h := &upgradeProbeHost{enabled: enabled}
	h.syncProposeFn = func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
		h.proposed = append(h.proposed, append([]byte(nil), cmd...))
		return sm.Result{Value: 1}, nil
	}
	return h
}

// putProbe puts key through both the base KV service and the MVCC service
// and returns the opcodes they proposed.
func putProbe(t *testing.T, h *upgradeProbeHost, key string) (kvOp, mvccOp byte) {
	t.Helper()
	ctx := context.Background()
	if err := NewKVService(h, 1).Put(ctx, key, []byte("v")); err != nil {
		t.Fatalf("KVService.Put(%q): %v", key, err)
	}
	if err := NewMVCCKVService(h, 1).Put(ctx, key, []byte("v")); err != nil {
		t.Fatalf("MVCCKVService.Put(%q): %v", key, err)
	}
	if len(h.proposed) != 2 {
		t.Fatalf("proposed %d commands, want 2", len(h.proposed))
	}
	kvOp, mvccOp = h.proposed[0][0], h.proposed[1][0]
	h.proposed = nil
	return kvOp, mvccOp
}

func TestUpgradeProbe_PlainPutUntilEnabled(t *testing.T) {
	h := newUpgradeProbeHost(false)
	kvOp, mvccOp := putProbe(t, h, "upgrade-probe/k")
	if kvOp != kv.OpPut || mvccOp != protocol.OpMVCCPut {
		t.Fatalf("opcodes before activation = %#x, %#x, want %#x, %#x", kvOp, mvccOp, kv.OpPut, protocol.OpMVCCPut)
	}
	if len(h.asked) == 0 || h.asked[0] != featuregate.UpgradeProbe {
		t.Fatalf("gate asked %v, want %s", h.asked, featuregate.UpgradeProbe)
	}
}

func TestUpgradeProbe_ProbeOpcodeOnceEnabled(t *testing.T) {
	h := newUpgradeProbeHost(true)
	for _, key := range []string{"upgrade-probe/k", "acme/orders/upgrade-probe/k"} {
		kvOp, mvccOp := putProbe(t, h, key)
		if kvOp != protocol.OpUpgradeProbe || mvccOp != protocol.OpUpgradeProbe {
			t.Fatalf("%s: opcodes after activation = %#x, %#x, want %#x", key, kvOp, mvccOp, protocol.OpUpgradeProbe)
		}
	}
	// The probe command carries the MVCC put payload.
	if err := NewKVService(h, 1).Put(context.Background(), "upgrade-probe/p", []byte("val")); err != nil {
		t.Fatal(err)
	}
	want, err := protocol.EncodeMVCCPut([]byte("upgrade-probe/p"), []byte("val"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.proposed[0]; string(got[1:]) != string(want[1:]) {
		t.Fatalf("probe payload = %x, want %x", got[1:], want[1:])
	}
}

func TestUpgradeProbe_OtherKeysUntouched(t *testing.T) {
	h := newUpgradeProbeHost(true)
	for _, key := range []string{"k", "upgrade-probe", "x/upgrade-probe/k", "acme/orders/k"} {
		kvOp, mvccOp := putProbe(t, h, key)
		if kvOp != kv.OpPut || mvccOp != protocol.OpMVCCPut {
			t.Fatalf("%s: opcodes = %#x, %#x, want plain puts", key, kvOp, mvccOp)
		}
	}
}

func TestUpgradeProbe_UngatedHostProposesPlainPut(t *testing.T) {
	h := &mockHost{}
	var op byte
	h.syncProposeFn = func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
		op = cmd[0]
		return sm.Result{Value: 1}, nil
	}
	if err := NewKVService(h, 1).Put(context.Background(), "upgrade-probe/k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if op != kv.OpPut {
		t.Fatalf("ungated host opcode = %#x, want %#x", op, kv.OpPut)
	}
}

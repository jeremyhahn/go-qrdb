//go:build qrdb_upgrade_probe

package store

import (
	"log/slog"

	"github.com/cockroachdb/pebble/v2"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// UpgradeProbeAppliedLog is the message a qrdb_upgrade_probe build logs, with
// the key, each time it applies protocol.OpUpgradeProbe, so the
// rolling-upgrade CLI e2e can see on every member which keys travelled as
// the gated command and which as a plain put.
const UpgradeProbeAppliedLog = "upgrade-probe: applied OpUpgradeProbe"

// upgradeProbeHandlers adds the rolling-upgrade matrix's synthetic N+1
// command: a qrdb_upgrade_probe build applies protocol.OpUpgradeProbe as an
// MVCC put and logs UpgradeProbeAppliedLog with the key.
func upgradeProbeHandlers() map[byte]pebbleUpdateHandler {
	return map[byte]pebbleUpdateHandler{
		protocol.OpUpgradeProbe: pebbleHandleUpgradeProbe,
	}
}

// pebbleHandleUpgradeProbe applies protocol.OpUpgradeProbe as an MVCC put and
// logs the applied key.
func pebbleHandleUpgradeProbe(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	res, notifs, err := pebbleHandleMVCCPut(s, batch, payload)
	if err == nil {
		if key, _, _, derr := protocol.DecodeMVCCPut(payload); derr == nil {
			slog.Info(UpgradeProbeAppliedLog, "key", string(key))
		}
	}
	return res, notifs, err
}

//go:build qrdb_upgrade_probe

package store

import "github.com/jeremyhahn/go-qrdb/pkg/protocol"

// upgradeProbeHandlers adds the rolling-upgrade matrix's synthetic N+1
// command: a qrdb_upgrade_probe build applies protocol.OpUpgradeProbe as an
// MVCC put.
func upgradeProbeHandlers() map[byte]pebbleUpdateHandler {
	return map[byte]pebbleUpdateHandler{
		protocol.OpUpgradeProbe: pebbleHandleMVCCPut,
	}
}

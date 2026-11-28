//go:build !qrdb_upgrade_probe

package store

// upgradeProbeHandlers is empty outside qrdb_upgrade_probe builds, so the
// state machine refuses protocol.OpUpgradeProbe as an invalid opcode.
func upgradeProbeHandlers() map[byte]pebbleUpdateHandler {
	return nil
}

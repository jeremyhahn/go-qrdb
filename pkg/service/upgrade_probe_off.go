//go:build !qrdb_upgrade_probe

package service

// upgradeProbeCommand returns cmd unchanged outside qrdb_upgrade_probe
// builds: only the rolling-upgrade matrix's synthetic N+1 proposes
// protocol.OpUpgradeProbe (upgrade_probe.go).
func upgradeProbeCommand(_ Host, _ uint64, _ string, _, cmd []byte) ([]byte, error) {
	return cmd, nil
}

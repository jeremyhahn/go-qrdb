//go:build qrdb_upgrade_probe

package service

import (
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// upgradeProbePrefix marks the keys whose puts a qrdb_upgrade_probe build
// proposes as protocol.OpUpgradeProbe once the feature is active.
const upgradeProbePrefix = "upgrade-probe/"

// upgradeProbeCommand is the rolling-upgrade matrix's synthetic N+1
// behaviour. A put of a key under "upgrade-probe/" (directly, or inside a
// "<tenant>/<database>/" namespace) is proposed as protocol.OpUpgradeProbe,
// with the EncodeMVCCPut payload, only once the group's feature gate
// reports every replica supports featuregate.UpgradeProbe -- the gating
// gatedCommandCap applies. Until then, and for every other key, cmd is
// proposed unchanged, so a mixed N/N+1 cluster never sees the new command.
func upgradeProbeCommand(host Host, shardID uint64, key string, value, cmd []byte) ([]byte, error) {
	if !isUpgradeProbeKey(key) {
		return cmd, nil
	}
	gated, ok := host.(FeatureGatedHost)
	if !ok || !gated.FeatureEnabled(shardID, featuregate.UpgradeProbe) {
		return cmd, nil
	}
	probe, err := protocol.EncodeMVCCPut([]byte(key), value, 0)
	if err != nil {
		return nil, err
	}
	probe[0] = protocol.OpUpgradeProbe
	return probe, nil
}

// isUpgradeProbeKey reports whether key lies under upgradeProbePrefix,
// either at the root of the keyspace or inside a database namespace.
func isUpgradeProbeKey(key string) bool {
	if strings.HasPrefix(key, upgradeProbePrefix) {
		return true
	}
	ns, ok := tenantid.NamespaceOf([]byte(key))
	return ok && strings.HasPrefix(key[len(ns):], upgradeProbePrefix)
}

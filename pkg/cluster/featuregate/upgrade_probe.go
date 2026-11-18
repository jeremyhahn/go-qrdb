//go:build qrdb_upgrade_probe

package featuregate

import "github.com/jeremyhahn/go-qrdb/pkg/feature"

// UpgradeProbe is the synthetic feature a qrdb_upgrade_probe build adds on
// top of the release it is built from: the "N+1" of the rolling-upgrade
// matrix (SPEC row rolling-upgrades, clauses 4 and 6). It gates
// protocol.OpUpgradeProbe, a Raft command an untagged ("N") binary refuses,
// so the matrix exercises a real new command behind the cluster feature
// gate. The tag is test-only: it is never in a release build.
const UpgradeProbe feature.Feature = "upgrade-probe"

func init() {
	supported = append(supported, UpgradeProbe)
}

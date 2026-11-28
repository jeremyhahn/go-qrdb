//go:build qrdb_upgrade_probe

package featuregate

import "github.com/jeremyhahn/go-qrdb/pkg/feature"

// A qrdb_upgrade_probe build supports UpgradeProbe after every released
// feature (upgrade_probe.go), so TestSupported wants it last.
func init() {
	testTaggedFeatures = []feature.Feature{UpgradeProbe}
}

// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// poolReady is qrdb_volume_pool_ready{pool}: 1 when this node's volume for
// pool is open and StateReady, 0 for any other state -- including a pool
// that never opened at boot. It is this node's own gauge, scraped locally;
// unlike volume.NodeReport it is never replicated and needs no lease, so a
// standalone node on the "file" or "objstore" backend (which keeps no
// leases and publishes no NodeReport, per engineering.md invariant 9)
// still exposes it.
var poolReady = sync.OnceValue(func() *metrics.GaugeVec {
	return metrics.NewGaugeVec(metrics.GaugeOpts{
		Namespace: "qrdb",
		Subsystem: "volume_pool",
		Name:      "ready",
		Help:      "1 if this node's volume for the pool is open and ready to serve, 0 otherwise.",
	}, []string{"pool"})
})

// RecordPoolHealth sets qrdb_volume_pool_ready{pool} from health: 1 only
// for StateReady, 0 for StateLocked, StateUnavailable, or a pool that
// never opened. Called at boot (once per configured pool) and again on
// every later health refresh, so a degraded boot's skipped pools -- and
// any pool that later becomes unavailable or locks on re-seal -- stay
// visible to Prometheus for as long as the condition lasts.
func RecordPoolHealth(pool string, health Health) {
	v := 0.0
	if health.State == StateReady {
		v = 1.0
	}
	poolReady().With(pool).Set(v)
}

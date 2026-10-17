// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// metricsGatherer returns what /metrics serves by default: the process
// registry (pkg/util/metrics, the qrdb_* series and the Go runtime) together
// with the shared go-quicraft collector's registry (the quicraft_* series).
// The process registry is read on every Gather, so a test that swaps it with
// metrics.SetRegistry is scraped as swapped.
func metricsGatherer() prometheus.Gatherer {
	return prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		return prometheus.Gatherers{metrics.Registry(), database.SharedRaftMetrics().Registry}.Gather()
	})
}

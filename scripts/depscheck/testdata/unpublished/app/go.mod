module example.com/app

go 1.27

// Like test/performance/cmd/kvcompare: app requires only perf, so the
// selected graph holds core at the v0.0.0 perf's go.mod requires.
require example.com/perf v1.0.0

module example.com/perf

go 1.27

// v0.0.0 is never published: perf builds against the local core, a
// replacement its consumers do not inherit.
require example.com/core v0.0.0

replace example.com/core => ../core

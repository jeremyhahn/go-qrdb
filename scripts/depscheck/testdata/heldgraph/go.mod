module example.com/heldgraph

go 1.25

require (
	example.com/a v1.0.0
	// example.com/b stays at v1.0.0: v1.1.0 changes the wire format.
	example.com/b v1.0.0
	example.com/c v1.0.0
)

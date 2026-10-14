//go:build clusterproof

// Package nemesis injects faults into the nodes of a cluster under proof and
// heals them again.
//
// A [Nemesis] is one kind of fault: process kill and restart, process pause,
// a network partition, a full disk, a slow disk or a clock running at the
// wrong rate. It never
// acts on the local host. Every fault goes through a [Target], which names a
// node and carries the two primitives a fault is built from: running a command
// on the node ([Target.Exec]) and signalling the node's process
// ([Target.Signal]). Two targets ship here: [LocalProcess], which only signals
// a process the harness started, and [RemoteTarget], whose transport is a set
// of command templates (for example a privileged one-shot fault container run
// through the container runtime over ssh).
//
// Heal is the contract that keeps a proof honest. It is idempotent, it heals
// every target the last Inject touched or tried to touch, and it runs after an
// Inject that failed. A fault that does not happen makes a proof vacuous; a
// fault that never heals destroys the cluster.
//
// A [Schedule] is a seeded, recorded sequence of fault windows, and a [Player]
// plays one against a set of nemeses, recording an [Event] for every step.
package nemesis

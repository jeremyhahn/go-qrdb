// Command leanstandalone is the smallest useful embedding of QRDB: one
// standalone node on the file storage backend, reached through the in-process
// (embedded) transport that sdk/go/node composes, with an entity DAO on top.
// No listener is opened and no Raft host is created.
//
// It is also the fixture of the lean-standalone build gate
// (test/buildgates/lean_standalone_test.go, `make check-lean-standalone`):
// whatever this program imports, an application embedding QRDB the same way
// compiles, so the gate reads this package's import graph and fails on any
// package such an application does not use -- the Raft host, PebbleDB, the
// network API servers, clustering, vectors, time series, metrics.
//
// Usage:
//
//	leanstandalone <data-dir>
package main

import (
	"context"
	"fmt"
	"os"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	qrdb "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
)

// note is the example's one entity type.
type note struct {
	ID   uint64 `json:"id"`
	Text string `json:"text"`
}

// EntityID returns the note's identifier, zero until it is first saved.
func (n *note) EntityID() uint64 { return n.ID }

// SetEntityID records the identifier Save assigns.
func (n *note) SetEntityID(id uint64) { n.ID = id }

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: leanstandalone <data-dir>")
		os.Exit(2)
	}
	if err := run(context.Background(), os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "leanstandalone:", err)
		os.Exit(1)
	}
}

// principal is the service identity the example's DAO calls run as. Every
// call through the embedded transport is authorized like a network call, so an
// embedder names a principal and grants it exactly the permissions it uses.
const principal = "leanstandalone"

func run(ctx context.Context, dataDir string) (err error) {
	qrdb.RegisterPermissionRoleGrant(principal,
		"kv:put", "kv:get", "kv:delete", "kv:list", "kv:exists", "kv:range",
		"index:create", "index:query", "index:scan", "index:list")

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = dataDir
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Backend: "file"}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false

	n, err := node.NewStandaloneNode(ctx, cfg, nil, node.WithServicePrincipal(principal))
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := n.Close(); err == nil {
			err = closeErr
		}
	}()
	if ctx, err = n.ServiceContext(ctx); err != nil {
		return err
	}

	client := n.Client()
	store, err := qrdb.NewKVStoreAdapter(client.KV(), client.Index())
	if err != nil {
		return err
	}
	notes, err := qrdb.NewDAO(store, "note", func() *note { return &note{} })
	if err != nil {
		return err
	}

	saved := &note{Text: "hello from a lean standalone node"}
	if err := notes.Save(ctx, saved); err != nil {
		return err
	}
	loaded, err := notes.Get(ctx, saved.ID)
	if err != nil {
		return err
	}
	fmt.Printf("note %d: %s\n", loaded.ID, loaded.Text)
	return nil
}

//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// daoCLIAccount is the entity a remote embedder keeps through the client
// DAO store: an ID, a unique Email index and a plain field.
type daoCLIAccount struct {
	ID    uint64 `json:"id"`
	Email string `json:"email" index:"unique"`
	Name  string `json:"name"`
}

func (a *daoCLIAccount) EntityID() uint64      { return a.ID }
func (a *daoCLIAccount) SetEntityID(id uint64) { a.ID = id }

// TestDAOClientStore_CLI_PerProtocol proves the client-backed DAO store
// (Client.KVStore, the store a remote embedder holds) and the qrdb CLI see
// the same records on every CLI-selectable transport: a DAO save is read by
// `qrdb kv get`, a `qrdb kv put` is found through the DAO's unique index and
// holds that unique value against a DAO save, and a `qrdb kv del` is
// observed by the DAO and releases the unique value. Embedded is not
// CLI-selectable; TestDAOStoreParity_ClientStore covers it in the SDK
// conformance matrix.
func TestDAOClientStore_CLI_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			client := sdkClientForProtocolAs(t, node, proto, node.clientCert, node.clientKey)
			defer client.Close()
			if err := client.Connect(ctx); err != nil {
				t.Fatalf("[%s] connect: %v", proto.name, err)
			}

			entityType := "daocli-" + proto.name
			accounts, err := sdk.NewDAO(client.KVStore(), entityType, func() *daoCLIAccount { return &daoCLIAccount{} })
			if err != nil {
				t.Fatalf("[%s] NewDAO: %v", proto.name, err)
			}
			key := func(id uint64) string { return fmt.Sprintf("%s/%d", entityType, id) }
			e1 := proto.name + "-one@example.test"
			e2 := proto.name + "-two@example.test"

			// (1) A DAO save is read back by the CLI on the same transport.
			if err := accounts.Save(ctx, &daoCLIAccount{ID: 1, Email: e1, Name: "one"}); err != nil {
				t.Fatalf("[%s] DAO Save(1): %v", proto.name, err)
			}
			out, err := runCLIProto(t, node, proto, "--output", "json", "kv", "get", key(1))
			if err != nil {
				t.Fatalf("[%s] kv get %s: %v\noutput: %s", proto.name, key(1), err, out)
			}
			var got daoCLIAccount
			if jerr := json.Unmarshal([]byte(cliKVValue(t, out)), &got); jerr != nil {
				t.Fatalf("[%s] decode kv get %s: %v\noutput: %s", proto.name, key(1), jerr, out)
			}
			if got.ID != 1 || got.Email != e1 || got.Name != "one" {
				t.Fatalf("[%s] kv get %s = %+v; want the DAO's entity 1", proto.name, key(1), got)
			}

			// (2) A CLI write is indexed by the node: the DAO finds it by its
			// unique value, and the value is held against a DAO save.
			body := fmt.Sprintf(`{"id":2,"email":%q,"name":"two"}`, e2)
			if out, err := runCLIProto(t, node, proto, "kv", "put", key(2), body); err != nil {
				t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, key(2), err, out)
			}
			found, err := accounts.QueryOneByIndex(ctx, "Email", e2)
			if err != nil || found.ID != 2 {
				t.Fatalf("[%s] QueryOneByIndex(Email, %s) = %+v, %v; want ID 2 (the CLI write)", proto.name, e2, found, err)
			}
			if err := accounts.Save(ctx, &daoCLIAccount{ID: 3, Email: e2, Name: "three"}); err == nil {
				t.Fatalf("[%s] DAO Save(3) with the CLI-written unique email succeeded; want a refusal", proto.name)
			}
			if out, err := runCLIProto(t, node, proto, "kv", "get", key(3)); err == nil {
				t.Fatalf("[%s] kv get %s after a refused save succeeded; want not found (output: %s)", proto.name, key(3), out)
			}

			// (3) A CLI delete is seen by the DAO and releases the unique value.
			if out, err := runCLIProto(t, node, proto, "kv", "del", key(2)); err != nil || !strings.Contains(out, "OK") {
				t.Fatalf("[%s] kv del %s: %v\noutput: %s", proto.name, key(2), err, out)
			}
			if _, err := accounts.Get(ctx, 2); !sdk.IsDAONotFound(err) {
				t.Fatalf("[%s] DAO Get(2) after kv del = %v; want not found", proto.name, err)
			}
			if err := accounts.Save(ctx, &daoCLIAccount{ID: 3, Email: e2, Name: "three"}); err != nil {
				t.Fatalf("[%s] DAO Save(3) after the CLI delete released %s: %v", proto.name, e2, err)
			}
			out, err = runCLIProto(t, node, proto, "--output", "json", "kv", "get", key(3))
			if err != nil {
				t.Fatalf("[%s] kv get %s: %v\noutput: %s", proto.name, key(3), err, out)
			}
			got = daoCLIAccount{}
			if jerr := json.Unmarshal([]byte(cliKVValue(t, out)), &got); jerr != nil || got.ID != 3 || got.Email != e2 {
				t.Fatalf("[%s] kv get %s = %+v (%v); want entity 3 with %s", proto.name, key(3), got, jerr, e2)
			}

			// (4) A unit of work commits two saves as one guarded batch over
			// this transport, and the CLI reads both back on the same one.
			txType := entityType + "-tx"
			txAccounts, err := sdk.NewDAOTransactional(client.KVStore(), txType, func() *daoCLIAccount { return &daoCLIAccount{} })
			if err != nil {
				t.Fatalf("[%s] NewDAOTransactional: %v", proto.name, err)
			}
			uow := sdk.NewDAOUnitOfWork(client.KVStore())
			want := []daoCLIAccount{
				{ID: 1, Email: proto.name + "-tx1@example.test", Name: "tx one"},
				{ID: 2, Email: proto.name + "-tx2@example.test", Name: "tx two"},
			}
			for i := range want {
				a := want[i]
				if err := sdk.RegisterDAOSave(uow, sdk.GenericDAO[*daoCLIAccount](txAccounts), &a); err != nil {
					t.Fatalf("[%s] RegisterDAOSave(%d): %v", proto.name, a.ID, err)
				}
			}
			if err := uow.Commit(ctx); err != nil {
				t.Fatalf("[%s] unit of work Commit: %v", proto.name, err)
			}
			for _, w := range want {
				txKey := fmt.Sprintf("%s/%d", txType, w.ID)
				out, err := runCLIProto(t, node, proto, "--output", "json", "kv", "get", txKey)
				if err != nil {
					t.Fatalf("[%s] kv get %s: %v\noutput: %s", proto.name, txKey, err, out)
				}
				got = daoCLIAccount{}
				if jerr := json.Unmarshal([]byte(cliKVValue(t, out)), &got); jerr != nil || got != w {
					t.Fatalf("[%s] kv get %s = %+v (%v); want the unit of work's %+v", proto.name, txKey, got, jerr, w)
				}
			}
		})
	}
}

// cliKVValue returns the stored value from `qrdb --output json kv get`
// output, which prints the value as one JSON string.
func cliKVValue(t *testing.T, out string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal([]byte(out), &value); err != nil {
		t.Fatalf("decode `kv get --output json` as a JSON string: %v\noutput: %s", err, out)
	}
	return value
}

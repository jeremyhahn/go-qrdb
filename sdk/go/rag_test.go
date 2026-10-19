//go:build qrdb_vector

package sdk

import "testing"

func TestClient_RAG_NilWhenTransportLacksIt(t *testing.T) {
	c := newPlainClient(t)

	if got := c.RAG(); got != nil {
		t.Fatalf("RAG = %v, want nil", got)
	}
}

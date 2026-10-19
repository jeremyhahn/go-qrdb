//go:build qrdb_vector

package sdk_test

import "testing"

func TestClient_RAG_ReturnsTheTransport(t *testing.T) {
	c, tr := newEmbeddedClient(t)

	if got := c.RAG(); any(got) != any(tr) {
		t.Fatalf("RAG = %T, want the embedded transport", got)
	}
}

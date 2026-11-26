package wireconflict

import "testing"

func TestMetadataRoundTrip(t *testing.T) {
	const msg = "unique index system/system/member.Email: value already taken"
	if got := Message(Metadata(msg)); got != msg {
		t.Fatalf("Message(Metadata(%q)) = %q", msg, got)
	}
}

func TestMessageMissingKey(t *testing.T) {
	if got := Message(nil); got != "" {
		t.Fatalf("Message(nil) = %q; want empty", got)
	}
}

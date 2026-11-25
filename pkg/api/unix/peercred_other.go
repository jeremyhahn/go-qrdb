//go:build !linux

package unix

import (
	"net"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// readPeerCredential reports no credential: the peer-credential identity is
// Linux-only (SO_PEERCRED). Elsewhere a certificate-less caller is refused.
func readPeerCredential(net.Conn) (auth.PeerCredential, bool) {
	return auth.PeerCredential{}, false
}

//go:build linux

package unix

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// readPeerCredential reads SO_PEERCRED from raw's socket: the uid, gid and
// pid of the process that connected, as the kernel recorded them at
// connect time.
func readPeerCredential(raw net.Conn) (auth.PeerCredential, bool) {
	sc, ok := raw.(syscall.Conn)
	if !ok {
		return auth.PeerCredential{}, false
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return auth.PeerCredential{}, false
	}
	var (
		ucred  *unix.Ucred
		optErr error
	)
	if err := rc.Control(func(fd uintptr) {
		ucred, optErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || optErr != nil || ucred == nil {
		return auth.PeerCredential{}, false
	}
	return auth.PeerCredential{UID: ucred.Uid, GID: ucred.Gid, PID: ucred.Pid}, true
}

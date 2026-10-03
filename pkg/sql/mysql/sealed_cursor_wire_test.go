// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build qrdb_mysql

package mysql

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	vtmysql "github.com/dolthub/vitess/go/mysql"
	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// cursorClient speaks just enough of the MySQL binary protocol to open a
// server-side cursor and fetch from it: COM_STMT_PREPARE, COM_STMT_EXECUTE
// with CURSOR_TYPE_READ_ONLY, COM_STMT_FETCH. The handshake is the vitess
// client's; the commands are written raw on the authenticated socket, because
// no Go driver opens a server-side cursor -- and COM_STMT_FETCH is answered
// inside vitess, never reaching the query handler a seal refuses through.
type cursorClient struct {
	conn net.Conn
}

// write sends one command packet; every command starts a new sequence.
func (c *cursorClient) write(payload []byte) error {
	hdr := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), 0}
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.conn.Write(append(hdr, payload...))
	return err
}

// read returns the next packet's payload, or the read error.
func (c *cursorClient) read() ([]byte, error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c.conn, hdr); err != nil {
		return nil, err
	}
	payload := make([]byte, int(hdr[0])|int(hdr[1])<<8|int(hdr[2])<<16)
	if _, err := io.ReadFull(c.conn, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (c *cursorClient) mustRead(t *testing.T) []byte {
	t.Helper()
	p, err := c.read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if p[0] == 0xff {
		t.Fatalf("server error: %q", p)
	}
	return p
}

// openCursor prepares query, executes it with a read-only cursor, and
// returns the statement ID.
func (c *cursorClient) openCursor(t *testing.T, query string, deprecateEOF bool) uint32 {
	t.Helper()
	require.NoError(t, c.write(append([]byte{0x16}, query...))) // COM_STMT_PREPARE
	ok := c.mustRead(t)
	stmtID := binary.LittleEndian.Uint32(ok[1:5])
	columns := int(binary.LittleEndian.Uint16(ok[5:7]))
	params := int(binary.LittleEndian.Uint16(ok[7:9]))
	for _, defs := range []int{params, columns} {
		for range defs {
			c.mustRead(t)
		}
		if defs > 0 && !deprecateEOF {
			c.mustRead(t)
		}
	}
	exec := []byte{0x17, 0, 0, 0, 0, 0x01, 1, 0, 0, 0} // COM_STMT_EXECUTE, CURSOR_TYPE_READ_ONLY, 1 iteration
	binary.LittleEndian.PutUint32(exec[1:5], stmtID)
	require.NoError(t, c.write(exec))
	c.mustRead(t) // column count
	defs := 0
	for p := c.mustRead(t); p[0] != 0xfe; p = c.mustRead(t) {
		defs++
	}
	require.Equal(t, columns, defs, "the cursor opened with the prepared statement's columns")
	return stmtID
}

// fetch asks the cursor for one row and returns the rows before the end
// packet, or the error the server or the socket answered.
func (c *cursorClient) fetch(stmtID uint32) (int, error) {
	req := []byte{0x1c, 0, 0, 0, 0, 1, 0, 0, 0} // COM_STMT_FETCH, 1 row
	binary.LittleEndian.PutUint32(req[1:5], stmtID)
	if err := c.write(req); err != nil {
		return 0, err
	}
	rows := 0
	for {
		p, err := c.read()
		if err != nil {
			return rows, err
		}
		switch p[0] {
		case 0xff:
			return rows, &cursorServerError{msg: string(p[3:])}
		case 0xfe:
			return rows, nil
		}
		rows++
	}
}

// cursorServerError is an error packet the server answered a fetch with.
type cursorServerError struct{ msg string }

func (e *cursorServerError) Error() string { return "server error: " + e.msg }

// cursorRows is the size of the table the cursor reads: more rows than the
// test fetches before its deadline, so the cursor cannot run dry.
const cursorRows = 1000

// cursorFixture starts a MySQL listener over a locking harness with a table of
// cursorRows rows, gated on probe and subscribed to streams.
func cursorFixture(t *testing.T, probe sealedWindowProbe, streams *clusterseal.Streams) (*cursorClient, bool) {
	t.Helper()
	h, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithServiceRegistry(h.Registry),
		WithSealedWindowProbe(probe),
		WithSealStreams(streams),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Stop)

	cfg := gomysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = srv.Addr()
	cfg.User = "qrdb"
	cfg.DBName = "qrdb"
	cfg.AllowNativePasswords = true
	cfg.Timeout = 30 * time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("CREATE TABLE t (id BIGINT PRIMARY KEY)")
	require.NoError(t, err)
	tx, err := db.Begin()
	require.NoError(t, err)
	insert, err := tx.Prepare("INSERT INTO t VALUES (?)")
	require.NoError(t, err)
	for i := 1; i <= cursorRows; i++ {
		_, err = insert.Exec(i)
		require.NoError(t, err)
	}
	require.NoError(t, insert.Close())
	require.NoError(t, tx.Commit())

	host, portStr, err := net.SplitHostPort(srv.Addr())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	vc, err := vtmysql.Connect(ctx, &vtmysql.ConnParams{Host: host, Port: port, Uname: "qrdb", DbName: "qrdb"})
	require.NoError(t, err)
	t.Cleanup(vc.Close)
	return &cursorClient{conn: vc.Conn}, vc.Capabilities&vtmysql.CapabilityClientDeprecateEOF != 0
}

// requireSealedEnd accepts only what a seal may produce: the server closed the
// connection, or refused the fetch as sealed. A fetch that hangs, or fails for
// another reason, is not the seal reaching the cursor.
func requireSealedEnd(t *testing.T, err error) {
	t.Helper()
	var serverErr *cursorServerError
	var netErr net.Error
	switch {
	case errors.As(err, &serverErr):
		if !strings.Contains(serverErr.msg, "sealed") {
			t.Fatalf("the fetch failed for another reason than the seal: %v", err)
		}
	case errors.As(err, &netErr) && netErr.Timeout():
		t.Fatalf("the fetch hung instead of failing: %v", err)
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, net.ErrClosed),
		errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
	default:
		t.Fatalf("the fetch failed with an unexpected error: %v", err)
	}
}

// TestOpenCursor_SealClosesTheConnection: a server-side cursor opened before a
// seal does not keep serving rows after it. COM_STMT_FETCH is answered inside
// vitess and never reaches the statement gate, so the seal guard closes the
// connection: the next fetch fails. It serves consecutive fetches before the
// seal, and it runs race-clean only because the server disables
// go-mysql-server's connection watcher, which would Peek the socket while
// vitess reads the fetch.
func TestOpenCursor_SealClosesTheConnection(t *testing.T) {
	cases := map[string]func(*fakeSealedProbe, *clusterseal.Streams){
		"node probe closes": func(p *fakeSealedProbe, _ *clusterseal.Streams) { p.sealed.Store(true) },
		"cluster seal ends the session": func(p *fakeSealedProbe, s *clusterseal.Streams) {
			p.sealed.Store(true) // the gate closes first, as the monitor closes it
			s.EndAll(&clusterseal.SealedError{Barrier: clusterseal.NodeBarrier()})
		},
	}
	for name, seal := range cases {
		t.Run(name, func(t *testing.T) {
			probe := &fakeSealedProbe{}
			streams := clusterseal.NewStreams()
			c, deprecateEOF := cursorFixture(t, probe, streams)
			stmt := c.openCursor(t, "SELECT id FROM t ORDER BY id", deprecateEOF)
			for range 3 {
				if rows, err := c.fetch(stmt); err != nil || rows != 1 {
					t.Fatalf("fetch before the seal = %d rows, %v; want 1 row", rows, err)
				}
			}
			seal(probe, streams)
			deadline := time.Now().Add(10 * time.Second)
			for {
				rows, err := c.fetch(stmt)
				if err != nil {
					requireSealedEnd(t, err)
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("the cursor kept serving after the seal (last fetch: %d rows)", rows)
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
	}
}

//go:build integration

package integration_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
)

// upgradeSQLWire names one SQL wire listener every upgrade-cluster node
// serves. docs/SPEC.md clause (4) and (6) of the rolling-upgrade row put the
// SQL wires in T2 beside the CLI transports: a mixed N/N+1 cluster must serve
// and forward writes over MySQL and PostgreSQL too.
type upgradeSQLWire string

const (
	sqlWireMySQL      upgradeSQLWire = "mysql"
	sqlWirePostgreSQL upgradeSQLWire = "postgresql"

	// sqlWireOpTimeout bounds one connect-and-statement round trip.
	sqlWireOpTimeout = 10 * time.Second
)

// upgradeSQLWires is every SQL wire the mixed-version matrix drives.
var upgradeSQLWires = []upgradeSQLWire{sqlWireMySQL, sqlWirePostgreSQL}

// sqlTLS is the mTLS client configuration for node idx's SQL listeners: the
// cluster CA and the admin certificate bound on that node.
func (c *upgradeCluster) sqlTLS(t *testing.T, idx int) *tls.Config {
	t.Helper()
	node := c.nodes[idx]
	return &tls.Config{
		RootCAs:      c.ca.Pool,
		Certificates: []tls.Certificate{loadTLSCert(t, node.clientCert, node.clientKey)},
		MinVersion:   tls.VersionTLS13,
	}
}

// sqlPut inserts key=value into the kv table over wire on node idx, on a
// fresh connection so a leadership change between calls is never hidden
// behind a pooled one.
func (c *upgradeCluster) sqlPut(t *testing.T, idx int, wire upgradeSQLWire, key, value string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), sqlWireOpTimeout)
	defer cancel()
	switch wire {
	case sqlWireMySQL:
		db, err := c.openMySQL(t, idx)
		if err != nil {
			return err
		}
		defer db.Close()
		_, err = db.ExecContext(ctx, "INSERT INTO kv (`key`, `value`) VALUES (?, ?)", key, []byte(value))
		return err
	case sqlWirePostgreSQL:
		conn, err := c.openPgSQL(ctx, t, idx)
		if err != nil {
			return err
		}
		defer conn.Close(context.Background())
		_, err = conn.Exec(ctx, "INSERT INTO kv (key, value) VALUES ($1, $2)", key, []byte(value))
		return err
	}
	return fmt.Errorf("unknown SQL wire %q", wire)
}

// sqlGet reads key's value from the kv table over wire on node idx.
func (c *upgradeCluster) sqlGet(t *testing.T, idx int, wire upgradeSQLWire, key string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), sqlWireOpTimeout)
	defer cancel()
	var value []byte
	switch wire {
	case sqlWireMySQL:
		db, err := c.openMySQL(t, idx)
		if err != nil {
			return "", err
		}
		defer db.Close()
		err = db.QueryRowContext(ctx, "SELECT `value` FROM kv WHERE `key` = ?", key).Scan(&value)
		return string(value), err
	case sqlWirePostgreSQL:
		conn, err := c.openPgSQL(ctx, t, idx)
		if err != nil {
			return "", err
		}
		defer conn.Close(context.Background())
		err = conn.QueryRow(ctx, "SELECT value FROM kv WHERE key = $1", key).Scan(&value)
		return string(value), err
	}
	return "", fmt.Errorf("unknown SQL wire %q", wire)
}

// openMySQL opens a single-connection MySQL pool to node idx as the bound
// admin.
func (c *upgradeCluster) openMySQL(t *testing.T, idx int) (*sql.DB, error) {
	t.Helper()
	tlsName := fmt.Sprintf("upgrade-node%d-%d", idx+1, time.Now().UnixNano())
	if err := gomysql.RegisterTLSConfig(tlsName, c.sqlTLS(t, idx)); err != nil {
		return nil, err
	}
	t.Cleanup(func() { gomysql.DeregisterTLSConfig(tlsName) })
	db, err := sql.Open("mysql", fmt.Sprintf("%s@tcp(127.0.0.1:%d)/%s?tls=%s",
		adminUser, c.mysqlPorts[idx], sqlClusterDatabase, tlsName))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// openPgSQL connects to node idx's PostgreSQL listener as the bound admin.
func (c *upgradeCluster) openPgSQL(ctx context.Context, t *testing.T, idx int) (*pgx.Conn, error) {
	t.Helper()
	cfg, err := pgx.ParseConfig(fmt.Sprintf("host=127.0.0.1 port=%d dbname=%s user=%s sslmode=require",
		c.pgsqlPorts[idx], sqlClusterDatabase, adminUser))
	if err != nil {
		return nil, err
	}
	cfg.TLSConfig = c.sqlTLS(t, idx)
	cfg.TLSConfig.ServerName = "localhost"
	cfg.Fallbacks = nil
	return pgx.ConnectConfig(ctx, cfg)
}

// sqlPutAcked writes key=value over wire through node idx until the write is
// acknowledged. A retry after an ambiguous failure (the statement may have
// committed before the error reached the client) counts as acknowledged
// once the value reads back through the same node and wire.
func (c *upgradeCluster) sqlPutAcked(t *testing.T, idx int, wire upgradeSQLWire, key, value string) {
	t.Helper()
	var err error
	if !pollUntil(60*time.Second, 200*time.Millisecond, func() bool {
		if err = c.sqlPut(t, idx, wire, key, value); err == nil {
			return true
		}
		got, getErr := c.sqlGet(t, idx, wire, key)
		return getErr == nil && got == value
	}) {
		t.Fatalf("node %d [%s]: INSERT %s never acknowledged: %v", idx+1, wire, key, err)
	}
}

// requireSQLReadsEverywhere fails the test unless every key in want reads
// back its value on every node over every SQL wire.
func (c *upgradeCluster) requireSQLReadsEverywhere(t *testing.T, want map[string]string) {
	t.Helper()
	for i := range c.nodes {
		for _, wire := range upgradeSQLWires {
			for key, value := range want {
				var got string
				var err error
				if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
					got, err = c.sqlGet(t, i, wire, key)
					return err == nil && got == value
				}) {
					if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
						err = fmt.Errorf("no row")
					}
					t.Fatalf("node %d [%s]: SELECT %s never read %q: got %q, %v", i+1, wire, key, value, got, err)
				}
			}
		}
	}
}

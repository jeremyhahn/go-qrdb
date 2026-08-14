package privprobe

import (
	"context"
	"fmt"
	"testing"

	gmssql "github.com/dolthub/go-mysql-server/sql"
	ast "github.com/dolthub/vitess/go/vt/sqlparser"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

type recHandler struct{ t *testing.T }

func (recHandler) CreateHandler(cat gmssql.Catalog) gmssql.AuthorizationHandler { return nil }

func TestProbe(t *testing.T) {
	h := sqltest.NewHarness()
	prov := provider.NewProviderWithEngine("probedb", h.KVSvc, database.EngineKV, provider.WithRegistry(h.Registry))
	eng := engine.New(engine.Config{Provider: prov})
	base := gmssql.NewBaseSessionWithClientServer("embedded", gmssql.Client{User: "alice", Address: "embedded"}, 1)
	base.SetCurrentDatabase("probedb")
	sess := provider.NewSession(base, &auth.Identity{Subject: "alice", TenantID: "acme"})
	ctx := gmssql.NewContext(context.Background(), gmssql.WithSession(sess))
	ctx.SetCurrentDatabase("probedb")

	for _, q := range []string{
		"CREATE TABLE users (id BIGINT PRIMARY KEY, name VARCHAR(64))",
		"GRANT SELECT ON probedb.users TO 'bob'@'%'",
		"GRANT SELECT, INSERT ON probedb.* TO 'bob'@'%'",
		"GRANT ALL PRIVILEGES ON *.* TO 'bob'@'%'",
		"SHOW GRANTS FOR 'bob'@'%'",
		"REVOKE SELECT ON probedb.users FROM 'bob'@'%'",
	} {
		_, iter, _, err := eng.Query(ctx, q)
		if err != nil {
			t.Logf("QUERY %-60s ERR %T %v", q, err, err)
			continue
		}
		rows, err := gmssql.RowIterToRows(ctx, iter)
		t.Logf("QUERY %-60s OK rows=%v err=%v", q, rows, err)
	}
	_ = fmt.Sprint
	_ = ast.AuthType_SELECT
	_ = service.Compare{}
}

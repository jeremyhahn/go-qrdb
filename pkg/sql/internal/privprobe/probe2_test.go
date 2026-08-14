package privprobe

import (
	"testing"

	ast "github.com/dolthub/vitess/go/vt/sqlparser"
)

func TestProbeParse(t *testing.T) {
	for _, q := range []string{
		"GRANT SELECT ON probedb.users TO 'bob'@'%'",
		"GRANT SELECT, INSERT ON probedb.* TO 'bob'@'%'",
		"GRANT ALL PRIVILEGES ON *.* TO 'bob'@'%'",
		"GRANT SELECT ON users TO 'bob'",
		"GRANT SELECT (id, name) ON probedb.users TO 'bob'@'%'",
		"GRANT SELECT ON probedb.users TO 'bob'@'%' WITH GRANT OPTION",
		"GRANT SUPER ON *.* TO 'bob'@'%'",
		"GRANT 'r1' TO 'bob'@'%'",
		"GRANT SELECT ON PROCEDURE probedb.p TO 'bob'@'%'",
		"REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'bob'@'%'",
		"REVOKE SELECT ON probedb.users FROM 'bob'@'%'",
		"SHOW GRANTS",
		"SHOW GRANTS FOR 'bob'@'localhost'",
	} {
		stmt, err := ast.Parse(q)
		if err != nil {
			t.Logf("%-60s PARSE ERR %v", q, err)
			continue
		}
		switch n := stmt.(type) {
		case *ast.GrantPrivilege:
			t.Logf("%-60s GrantPriv level=%+v privs=%+v objtype=%v to=%+v wgo=%v auth=%+v", q, n.PrivilegeLevel, n.Privileges, n.ObjectType, n.To, n.WithGrantOption, n.Auth)
		case *ast.RevokePrivilege:
			t.Logf("%-60s RevokePriv level=%+v privs=%+v objtype=%v from=%+v auth=%+v", q, n.PrivilegeLevel, n.Privileges, n.ObjectType, n.From, n.Auth)
		case *ast.ShowGrants:
			t.Logf("%-60s ShowGrants for=%+v using=%+v", q, n.For, n.Using)
		default:
			t.Logf("%-60s type=%T", q, stmt)
		}
	}
}

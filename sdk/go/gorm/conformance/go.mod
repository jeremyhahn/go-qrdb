// Module github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance is a nested
// module deliberately kept separate from sdk/go: it pulls in gorm.io/driver/
// {mysql,postgres,gaussdb}, lib/pq, and the upstream gorm.io/gorm test
// fixtures purely to run GORM's own conformance suite against qrdb, and none
// of that belongs in sdk/go's dependency graph (every SDK consumer would
// otherwise inherit it). Being a separate module also keeps this package
// invisible to `go test ./...` at the repository root and to `make
// test-sdk`, so a red conformance run can never fail either of those.
//
// Like the root module, no local-path replace directives are committed
// here: local development resolution against sdk/go and go-qrdb is provided
// entirely by the top-level Go workspace (/home/jhahn/sources/go.work),
// which must list this directory in its `use` block. See README.md.
module github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance

go 1.26.5

require (
	github.com/go-sql-driver/mysql v1.9.3
	github.com/google/uuid v1.6.0
	github.com/jeremyhahn/go-qrdb/sdk/go v0.0.0
	github.com/jinzhu/now v1.1.5
	github.com/lib/pq v1.12.3
	github.com/stretchr/testify v1.11.1
	gorm.io/driver/gaussdb v0.1.0
	gorm.io/driver/mysql v1.6.0
	gorm.io/driver/postgres v1.6.2
	gorm.io/gorm v1.31.2
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/HuaweiCloudDeveloper/gaussdb-go v1.0.0-rc1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/tjfoc/gmsm v1.4.1 // indirect
	golang.org/x/crypto v0.49.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/text v0.35.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

module github.com/jeremyhahn/go-quicraft/contrib/qrdb/test/performance/qrdb

go 1.26

toolchain go1.26.0

require (
	github.com/jeremyhahn/go-quicraft/test/performance v0.0.0
	github.com/quic-go/quic-go v0.59.0
)

require (
	github.com/quic-go/qpack v0.6.0 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.34.0 // indirect
)

replace (
	github.com/jeremyhahn/go-quicraft => ../../../../..
	github.com/jeremyhahn/go-quicraft/test/performance => ../../../../../test/performance
)

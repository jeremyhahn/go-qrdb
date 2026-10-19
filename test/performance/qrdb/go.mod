module github.com/jeremyhahn/go-qrdb/test/performance/qrdb

go 1.27.2

require (
	// go-qrdb: held at v0.7.0-alpha. ./Dockerfile, the only build of this
	// module, replaces go-qrdb with the build context, so this line selects
	// nothing there. Moving it to the newest tag is pending a build of this
	// module against that tag's published source.
	github.com/jeremyhahn/go-qrdb v0.7.0-alpha
	// sdk/go: held at v0.7.0-alpha, with go-qrdb above. In ./Dockerfile the
	// replaced go-qrdb requires the newest sdk/go, which minimal version
	// selection prefers over this line, so it selects nothing there either.
	// Moving it to the newest tag is pending the same build.
	github.com/jeremyhahn/go-qrdb/sdk/go v0.7.0-alpha
	// go-quicraft/test/performance: the newest tag, in lockstep with the root
	// module's go-quicraft (#659).
	github.com/jeremyhahn/go-quicraft/test/performance v0.22.0-alpha
	github.com/quic-go/quic-go v0.64.1
	google.golang.org/grpc v1.84.0
)

require (
	github.com/quic-go/qpack v0.6.0 // indirect
	go.uber.org/mock v0.6.0 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261019180522-ce908d29299b // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

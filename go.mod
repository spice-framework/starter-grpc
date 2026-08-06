module github.com/spice-framework/starter-grpc

go 1.26.0

toolchain go1.26.5

require (
	github.com/spice-framework/spice v0.0.0-20260805222830-a2ecd56df246
	google.golang.org/grpc v1.83.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/spice-framework/development v0.0.0-20260806052122-9025218a91c0 // indirect
	github.com/spice-framework/toolchain v0.0.0-20260806054457-a83d9b58034c // indirect
	golang.org/x/mod v0.38.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
)

tool (
	github.com/spice-framework/development/cmd/spice-dev
	github.com/spice-framework/toolchain/cmd/spice-library-release-verify
)

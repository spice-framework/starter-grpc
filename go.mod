module github.com/spice-framework/starter-grpc

go 1.26.0

toolchain go1.26.5

require (
	github.com/spice-framework/spice v0.0.0-20260805175412-383c17744300
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/spice-framework/development v0.0.0-20260806034648-1856466df09d // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
)

tool github.com/spice-framework/development/cmd/spice-dev

# Dependency review: grpc-go

- Decision: approved for the isolated `starter/grpc` package.
- Version: `google.golang.org/grpc` v1.82.1.
- Upstream: <https://github.com/grpc/grpc-go>.
- License: Apache-2.0; retained with the mechanically vendored source.
- Maintenance: grpc-go is the official actively maintained Go implementation
  of gRPC. The selected release was published July 15, 2026, supports Go 1.25+,
  and is compatible with Spice's mandatory Go 1.26.5 toolchain.
- Dependency scope: grpc-go brings the official protobuf runtime plus bounded
  `x/net` HTTP/2 and Google RPC status packages. Spice does not adopt protoc or
  its plugins as runtime dependencies and does not create an alternative IDL
  toolchain.
- Security: server TLS 1.2+ with an explicit certificate is the default;
  clients use verified TLS and target-hostname validation. Plaintext requires a
  local-development opt-out. Message sizes, concurrent streams, targets,
  service names, and registration counts are bounded. Reflection and arbitrary
  native server options are not enabled by the starter.
- Cancellation: every RPC remains caller-context-owned. Server cleanup attempts
  graceful drain, force-stops when its lifecycle context expires, and reports
  the cancellation cause. Client construction is lazy and cleanup is
  idempotent.
- Observability: unary and streaming client/server interceptors expose only
  method identity, direction, kind, status code, and duration. They never
  receive a payload or credential field through the Spice observer contract.
- Configuration: `OpenServer` does not bind; `OpenClient` does not connect.
  Applications explicitly own listeners, registrations, generated clients,
  credentials, and service policy. There is no package-presence activation,
  global registry, or hidden module download.
- Verification: race-enabled loopback tests exercise a registered RPC, standard
  health, client/server observations, graceful cleanup, forced cancellation,
  TLS defaults, invalid registrations, and panic redaction. Real cross-process
  mTLS acceptance remains broader integration work.

Primary references:

- <https://github.com/grpc/grpc-go/releases/tag/v1.82.1>
- <https://pkg.go.dev/google.golang.org/grpc@v1.82.1>
- <https://grpc.io/docs/languages/go/basics/>
- <https://github.com/grpc/grpc-go/blob/v1.82.1/LICENSE>

# Dependency review: grpc-go

- Decision: approved for the isolated
  `github.com/spice-framework/starter-grpc` module.
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
- Verification: race-enabled local TCP acceptance uses a locally issued CA,
  verified server TLS, and required client certificates. It exercises RPCs,
  standard health, client/server observations, concurrency, message limits,
  graceful cleanup, forced cancellation, invalid registrations, and diagnostic
  redaction without external network access.
- Spice compatibility: the module selects the provisional minimum. The strict
  repository compatibility manifest and isolated CI matrix verify distinct
  minimum and current revisions with exact MVS selection; the starter manifest
  independently requires the exact Spice starter API.

## Build-only dependencies: Spice release tools

- Decision: approved only as the repository-authorized release-parity tool.
- Signer version: `github.com/spice-framework/development`
  `v0.0.0-20260806052122-9025218a91c0`.
- Signer tool: `github.com/spice-framework/development/cmd/spice-dev` through the
  standard Go `tool` directive; invocations always use the full package path.
- Verifier version: `github.com/spice-framework/toolchain`
  `v0.0.0-20260806054457-a83d9b58034c`.
- Verifier tool:
  `github.com/spice-framework/toolchain/cmd/spice-library-release-verify`.
- License: Apache-2.0, with its notice retained in `vendor`.
- Runtime scope: none. Product packages do not import the development module,
  and released applications acquire no runtime dependency on it.
- Dependency graph: the tool participates in normal Go minimal-version
  selection. That build-time coupling is accepted and visible in `go.mod`,
  `go.sum`, and `vendor/modules.txt`; no parallel tool registry is introduced.
- Integrity and network behavior: the exact pseudo-version is pinned and
  checksummed. Release parity runs with `GOWORK=off`, `GOPROXY=off`,
  `GOTOOLCHAIN=local`, and `GOFLAGS=-mod=vendor`, so it cannot select an ambient
  checkout, upgrade itself, or download dependencies.
- Security: the trusted native tool reads the exact committed Git graph and
  writes only to caller-supplied temporary output directories. The rehearsal
  emits no signatures or signing material.
- Maintenance: the retained local builder and production signing workflow stay
  in place. A dual-builder gate detects central renderer regressions before any
  future authority migration.

Primary references:

- <https://github.com/grpc/grpc-go/releases/tag/v1.82.1>
- <https://pkg.go.dev/google.golang.org/grpc@v1.82.1>
- <https://grpc.io/docs/languages/go/basics/>
- <https://github.com/grpc/grpc-go/blob/v1.82.1/LICENSE>

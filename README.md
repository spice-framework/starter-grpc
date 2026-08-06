# Spice gRPC starter

`github.com/spice-framework/starter-grpc` is the independently versioned,
opt-in gRPC client/server integration for Spice. It wraps the official grpc-go
runtime with explicit configuration, bounded resource use, lifecycle cleanup,
and payload-free observations. Importing Spice core alone never starts a
server, opens a connection, registers reflection, or selects gRPC.

```go
server, cleanup, err := spicegrpc.OpenServer(
    spicegrpc.ServerConfig{
        TLSConfig:    serverTLS,
        EnableHealth: true,
    },
    []spicegrpc.Registration{{
        Service: "orders.v1.Orders",
        Register: func(registrar grpc.ServiceRegistrar) error {
            ordersv1.RegisterOrdersServer(registrar, ordersService)
            return nil
        },
    }},
    observer,
)
```

`OpenServer` validates and registers generated services without binding or
starting background work. The application owns the listener and calls `Serve`.
Cleanup first drains active RPCs and force-stops only when its caller-owned
context expires. The optional standard health service is explicitly enabled.

`OpenClient` creates a lazy, instance-owned grpc-go connection. TLS 1.2+
certificate and hostname verification are the defaults. Mutual TLS is ordinary
`tls.Config` with caller-owned roots and client certificates. Plaintext requires
an explicit `AllowInsecure` opt-in intended only for isolated local tests.

Message sizes, concurrent streams, service counts, service names, and targets
are bounded. Client and server interceptor observations contain only direction,
RPC kind, full method, status code, and duration—never credentials, metadata,
request payloads, or responses.

## Install

```text
go get github.com/spice-framework/starter-grpc@latest
```

During preview development, applications should pin an exact compatible
revision recorded in [support metadata](docs/support.md). The strict
[`spice-compatibility.json`](spice-compatibility.json) contract declares
distinct minimum and current Spice revisions without inventing a runtime
dependency resolver.

## Verify

Go 1.26.5 is mandatory:

```text
make check
make acceptance
make compatibility
make release-parity
make verify
make verify-release
```

Acceptance uses local ephemeral TCP listeners and locally issued test
certificates. It proves verified TLS and mTLS, unary RPCs, standard health,
client/server interceptors, cancellation, graceful drain, forced cleanup,
message limits, diagnostic redaction, and concurrent calls without contacting
an external service.

The complete verifier checks formatting, module/vendor reproducibility, vet,
allowlisted lint and nil safety, gosec, govulncheck, shuffled race tests, at
least 85% product coverage, strict minimum/current core compatibility, and
offline vendor builds.

Release parity runs the exact `spice-dev` tool authorized by `go.mod` and the
retained repository builder twice each, entirely from `vendor` with network and
workspace resolution disabled. It requires byte-identical source archives,
fully validates their bounded gzip/TAR contents, compares equivalent SBOM
package and dependency facts, verifies canonical checksum files, and forbids
rehearsal signatures on Windows and Linux.

See [the dependency review](docs/dependency-review.md) and
[support contract](docs/support.md) before production adoption.

## Releases

Each version tag is an ordinary Go module release. The repository also builds
an exact-commit source archive, committed-graph SPDX 2.3 SBOM, SHA-256
checksums, and an Ed25519 signature/public key without an external release
build system. Production mode requires a clean checkout, exact tag, and
protected signing key; an explicit unsigned rehearsal is available for local
proof. See [`docs/releasing.md`](docs/releasing.md) for the artifact and trust
contract. The protected central workflow is the release authority. The retained
repository builder remains only an unsigned parity oracle and is held to the
dual-builder contract during the migration.

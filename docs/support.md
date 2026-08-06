# Support and compatibility

| Contract | Current development support |
|---|---|
| Go | Exactly 1.26.5 for development and release verification |
| Spice minimum/current | Exact versions in [`spice-compatibility.json`](../spice-compatibility.json) |
| Spice starter API | Exact `v1alpha1`; mismatches fail closed |
| grpc-go | `google.golang.org/grpc` v1.82.1 |
| Operating systems | Windows, Linux, and macOS |
| Architectures | amd64 and arm64 compilation through the public core API |
| Transport | TLS 1.2+ by default; explicit plaintext only for isolated local development |
| Service model | Caller-owned generated registrations, listeners, clients, contexts, and cleanup |
| Release signer | `github.com/spice-framework/development/cmd/spice-dev` at `v0.0.0-20260806121906-963bb6676069` |
| Independent verifier | `github.com/spice-framework/toolchain/cmd/spice-library-release-verify` at `v0.0.0-20260806054457-a83d9b58034c` |

[`spice-compatibility.json`](../spice-compatibility.json) is the sole preview
compatibility boundary. The committed module selects its provisional minimum;
the current value is a forward-compatibility endpoint, not an unbounded runtime
dependency. The repository-owned compatibility verifier resolves each boundary
through an isolated alternate modfile, requires exact MVS selection, runs vet
and shuffled race tests for every product package with `GOPROXY=off`, and hashes
the repository before and after to prove source, module, and vendor immutability.

Release artifacts are produced only from an exact tagged commit under the
contract in [`releasing.md`](releasing.md). A compromised or missing signing
secret fails a production release; it never falls back to unsigned output.

The pinned central signer and independent verifier power the protected reusable
production workflow. Windows and Linux CI still compare unsigned central and
retained outputs under vendor-only offline resolution; the retained command is
only a parity oracle. Production remains disabled until a reviewed
`security/release/ed25519-public.pem`, the per-repository
`SPICE_LIBRARY_RELEASE_SIGNING_KEY`, and protected `release-signing` and
`release-publish` environments are configured.

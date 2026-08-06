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
| Release signer | `github.com/spice-framework/development/cmd/spice-dev` at `v0.0.0-20260806132124-4c308d1b9fda` |
| Independent verifier | `github.com/spice-framework/toolchain/cmd/spice-library-release-verify` at `v0.0.0-20260806133530-71211498297c` |
| Public trust anchor | [`security/release/ed25519-public.pem`](../security/release/ed25519-public.pem), SHA-256 `4bc50198c65b1e4f542d16cda46ca0736c716bebff63ececce1ea53daf285621` |

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
production workflow. Windows and Linux CI render the same inert central plan
twice under vendor-only offline resolution and require byte-identical unsigned
artifacts. The reviewed public trust anchor is configured at
`security/release/ed25519-public.pem`; its fingerprint is the SHA-256 digest of
the DER SubjectPublicKeyInfo bytes. The matching private key is stored only as
the repository Actions secret `SPICE_LIBRARY_RELEASE_SIGNING_KEY` and passed
through the exact one-name caller mapping. The protected `release-signing`
environment remains the human approval gate and contains no key. These
configured controls do not establish that a tag or published release exists.

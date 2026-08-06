# Release contract

starter-grpc releases are ordinary Go module tags plus a small, independently
verifiable artifact set. The repository owns the release contract while the
organization-owned reusable workflow performs the common build, signing,
independent verification, and publication phases. No mutable workspace snapshot
or network-resolved package list participates in artifact construction.

For `v1.2.3`, the release builder produces:

| Artifact | Contract |
|---|---|
| `starter-grpc_1.2.3_source.tar.gz` | Exact tagged Git commit, under one versioned directory |
| `starter-grpc_1.2.3_sbom.spdx.json` | SPDX 2.3 packages from the consistent committed `go.mod`, `go.sum`, and `vendor/modules.txt` graph |
| `checksums.txt` | SHA-256 of the source archive and SBOM, sorted by filename |
| `checksums.txt.sig` | Raw Ed25519 signature of the exact checksum bytes |
| `checksums.txt.pem` | X.509 SubjectPublicKeyInfo PEM for signature verification |

The source archive is reconstructed from the full commit's `git ls-tree`
identity and exact object bytes read through `git cat-file --batch`. It never
uses checkout filters or `git archive`, so `core.autocrlf` and host line-ending
settings cannot alter an artifact. Every tar and gzip timestamp is the source
commit epoch; paths are relative, ownership is zeroed, executable modes and
validated symlinks are preserved, and gzip output is deterministic. Gitlinks
and unsupported modes fail closed. Dirty or untracked workspace files cannot
enter the archive. The SBOM creation time uses the same epoch and contains no
absolute checkout path. Construction fails when committed module selection,
checksums, and vendored versions or replacements disagree; the builder does
not rely on an earlier verifier to detect a stale dependency graph.

## Production ceremony

Production releases call the organization-owned reusable workflow at an
immutable commit. Before any release tag is created, a release owner must:

1. generate a user-owned Ed25519 private key dedicated to this repository;
2. review and commit its public key as
   `security/release/ed25519-public.pem`;
3. store the private key as `SPICE_LIBRARY_RELEASE_SIGNING_KEY` only in the
   protected `release-signing` environment; and
4. configure protected `release-signing` and `release-publish` environments
   with the required human reviewers.

Do not create or push a release tag until all four controls exist. The caller
maps no secrets. The reusable workflow obtains the signing key only from its
`release-signing` job, validates the exact tag and public trust anchor, signs
with the centrally pinned tool, independently verifies with the separately
pinned verifier, and publishes only through `release-publish`. A missing key,
anchor, environment, review, or verification result fails closed.

## Unsigned rehearsal

An explicit rehearsal exercises the exact same source archive, SBOM, and
checksum pipeline without requiring a clean checkout or matching tag:

```text
go run ./cmd/starter-grpc-release \
  -rehearsal \
  -version=v0.0.0-rehearsal \
  -output=dist-rehearsal
```

Rehearsals are always unsigned and always archive `HEAD`, never working-tree
contents. Passing a signing key together with `-rehearsal` is rejected.

## Unsigned dual-builder rehearsal

The root `go.mod` authorizes the exact central renderer through a standard Go
`tool` directive. `make release-parity` runs that fully qualified tool and the
retained repository builder twice each with `GOWORK=off`, `GOPROXY=off`,
`GOTOOLCHAIN=local`, and `GOFLAGS=-mod=vendor`. The central tool first emits a
read-only plan bound to the committed module and compatibility metadata, then
renders only to caller-owned temporary directories.

The central renderer and signer are the production implementation. The retained
repository builder remains only an unsigned parity oracle during the migration:

```text
make release-parity
```

Both rehearsals are unsigned, deterministic across two independent outputs,
and archive the exact committed `HEAD` tree beneath the identical
`starter-grpc_VERSION/` root. Parity requires byte-identical compressed source
archives and also fully drains and decodes both streams. It bounds compressed
and expanded data, validates canonical gzip metadata and CRC/trailer handling,
rejects extra gzip members, raw trailing bytes, and bytes hidden after the TAR
end markers, and compares every entry's path, order, type, mode, link, size,
timestamp, PAX metadata, and content hash.

The SPDX documents must match exactly outside these builder-provenance fields:

- document name (`Spice gRPC starter VERSION` retained and
  `starter-grpc VERSION` centrally);
- namespace identity, including the central `spdx/v1/` contract segment; and
- creator organization/tool (`Spice Authors` and the retained command versus
  `Spice Framework` and renderer/v1).

Every package, external reference, relationship, creation time, and other
decoded field must match. Each checksum file must independently and canonically
verify its own archive and SBOM. Extra artifacts, signatures, malformed line
endings or spacing, archive drift, and undocumented SBOM drift fail closed.

`make verify-release` runs this dual-builder proof after the complete repository
contract. The retained builder is not removed by this cutover and never receives
production signing authority; removal requires a separate reviewed change after
the central signed path has durable release evidence.

## Consumer verification

With OpenSSL 3 and GNU-compatible checksum tooling:

```text
sha256sum -c checksums.txt
openssl pkeyutl -verify -pubin -inkey checksums.txt.pem \
  -rawin -in checksums.txt -sigfile checksums.txt.sig
```

Consumers must authenticate `checksums.txt.sig` against the reviewed
`security/release/ed25519-public.pem` from the exact tagged source, not against a
public key supplied only beside release assets. Until that trust anchor and the
protected environments are configured, this repository must not publish a tag.

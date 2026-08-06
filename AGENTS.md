# Starter gRPC implementation contract

This repository owns the independently versioned gRPC client/server integration
for Spice. Work directly on local `main` in bounded commits. Fetch before
editing and immediately before pushing; never overwrite unexpected remote work.

Go 1.26.5 is mandatory. Every product change must preserve TLS 1.2+ secure
defaults, explicit local-only plaintext opt-in, caller-owned listeners,
contexts, connections, and registrations, bounded messages/streams/services,
idempotent cleanup, graceful drain with forced cancellation, and payload- and
credential-free diagnostics and observations. There must be no global server,
reflection service, hidden dialing, arbitrary native option injection, or
automatic module/tool download.

Release-parity work must preserve the exact `spice-dev` tool version authorized
by the root `go.mod`, invoke its full package path, and run both central and
retained rehearsals with workspace and network resolution disabled in vendor
mode. The protected central workflow is the production authority; the retained
repository builder remains an unsigned parity oracle and must never receive
signatures or key material.

Add positive and failure-path tests, update public documentation, use focused
checks while iterating, run `make verify` once on the exact final tree, and push
only a green commit. Deterministic real local TLS and mutual-TLS acceptance is
mandatory and must not contact an external service. Never hand-edit vendor
files.

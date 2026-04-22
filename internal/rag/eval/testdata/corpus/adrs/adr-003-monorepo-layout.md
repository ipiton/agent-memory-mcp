# ADR 003: Monorepo layout for backend services

## Status

Accepted

## Context

The backend consists of about a dozen microservices that share a common
library of domain types, authentication helpers, and RPC contracts. Prior
to this decision each service lived in its own repository, which made
cross-service refactors painful and encouraged drift in shared code.

## Decision

Consolidate all backend services into a single Go monorepo with the
following structure:

- `cmd/` — one subdirectory per deployable binary.
- `internal/` — shared packages not consumed outside this repo.
- `pkg/` — stable packages that can be imported by other repositories.
- `proto/` — protobuf contracts versioned by directory.

Each service owns a `cmd/<service>/README.md` describing its purpose,
on-call owners, and deploy target.

## Consequences

- Atomic refactors that touch shared types and multiple services become
  trivial in a single PR.
- CI must be smart enough to only rebuild and test the affected subtree;
  we accept a one-off investment in a build cache.
- Release tagging moves to per-service tags rather than a single repo
  version — `service-name/v1.4.0` style tags.
- New engineers can read a single repo to understand the system,
  removing a significant onboarding hurdle.

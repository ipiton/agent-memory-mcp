# Architecture Overview

## High-level shape

The platform is a set of Go microservices deployed to Kubernetes. A thin
API gateway fronts the system, handles authentication via Zitadel OIDC,
applies rate limiting, and fans out requests to backend services.

## Core services

- `api-gateway`: ingress entrypoint, auth, rate limiting, observability.
- `catalog`: product catalog reads and writes.
- `orders`: order lifecycle, payments, inventory holds.
- `search`: full-text and semantic search over catalog data.
- `recommendations`: ML-backed recommendation fan-out.
- `notifications`: email, SMS, and push delivery.

Each service owns its database schema. Cross-service reads go through
published events on Kafka, never through direct database access.

## Data layer

- Primary relational store: PostgreSQL 15, one logical database per
  service.
- Cache: Redis Cluster, used for session tokens and hot catalog reads.
- Event bus: Kafka, with schema registry for message contracts.
- Object storage: S3-compatible bucket for user uploads and backups.

## Deployment

- Container orchestrator: Kubernetes 1.29 on managed cloud.
- GitOps: Argo CD reconciles every deployed release from git.
- CI: GitHub Actions for build, test, and image push.

## Cross-cutting concerns

- Observability stack documented in ADR 005 (logs in Loki, metrics in
  Prometheus/Thanos, traces in Tempo, UI in Grafana).
- All services share a common `platform-go` library for logging,
  tracing, and auth helpers.
- Every service exposes `/healthz`, `/readyz`, and `/metrics` on the
  same sidecar port.

## Non-goals

- Multi-region active-active is out of scope for the current cycle.
- We deliberately do not ship the platform to on-premise customers.

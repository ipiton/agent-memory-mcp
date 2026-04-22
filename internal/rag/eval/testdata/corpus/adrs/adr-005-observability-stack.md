# ADR 005: Observability stack

## Status

Accepted

## Context

Each service previously shipped its own ad-hoc logging, metrics, and
tracing setup. Operators lacked a consistent way to correlate signals
across services during incidents. We need a unified observability stack.

## Decision

Standardize on the following observability components:

- Logs: Fluent Bit agent on every node shipping to Loki.
- Metrics: Prometheus, with a central Thanos setup for long-term storage.
- Traces: OpenTelemetry SDK in every service, exporting to Tempo.
- Dashboards and alerting: Grafana.

All three signal types share a common correlation key: `trace_id`.

## Consequences

- A single Grafana URL is the starting point for all investigations.
- Engineers instrument services through a thin wrapper that enforces
  correlation: every log line includes `trace_id` and `span_id` when
  available.
- Retention policy: logs 30 days, metrics 400 days (via Thanos), traces
  14 days.
- Datadog and New Relic were rejected primarily on cost at our data
  volume, and secondarily to avoid vendor lock-in for tracing.

## Alerting philosophy

Alerts must be actionable and owner-tagged. Every alert links to a runbook.
Silent dashboards are considered technical debt.

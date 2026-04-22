# Postmortem: 2025-10-20 memory leak in search service

## Summary

On 2025-10-20 the search service started OOM-killing replicas every
90 minutes. Root cause was a goroutine leak introduced by the previous
day's deploy that launched a background retry worker on every request
without a shutdown signal.

## Impact

- Replicas restarting every 90 minutes caused intermittent `502` errors
  for roughly 0.3% of requests for 14 hours.
- No data loss.

## Timeline

- Day -1, 16:00 — Deploy of v1.14.0 introducing the new search feature.
- 02:45 UTC — First OOM kill observed in monitoring.
- 07:02 UTC — On-call notices pattern in replica restarts.
- 09:30 UTC — Rollback to v1.13.4 via the deploy rollback runbook.
- 09:41 UTC — Replica restarts stop. OOM kills cease.

## Root cause

The `searchRetryWorker` function was launched as a goroutine per request
but never received a done signal. Over ~90 minutes the goroutines
accumulated until each replica exceeded its 2GB memory limit.

## Contributing factors

- Code review missed the unbounded goroutine spawn.
- Pre-production load testing used 50 RPS, far below the steady-state
  production load of 4,000 RPS; the leak was invisible at that scale.
- No alert on goroutine count per replica.

## Remediation

- Added goroutine count panel and alert (`> 10k` triggers pager).
- Static analysis rule added to flag `go func()` without an explicit
  context.Context or done channel.
- Load test harness updated to target at least 2,000 RPS sustained
  for 4 hours as a standard pre-release gate.

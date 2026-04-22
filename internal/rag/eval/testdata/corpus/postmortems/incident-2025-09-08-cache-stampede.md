# Postmortem: 2025-09-08 cache stampede on catalog service

## Summary

On 2025-09-08 at 19:07 UTC a cache stampede against the product catalog
service caused PostgreSQL CPU to saturate for 22 minutes. p99 latency
went from 90ms to 3.4s across all read paths.

## Impact

- 22 minutes of elevated latency site-wide.
- 12,800 requests failed with gateway timeouts.
- No data loss.

## Timeline

- 19:03 — Redis replica failover begins after a scheduled patch window.
- 19:05 — Primary Redis briefly unavailable; cache keys evicted.
- 19:07 — Catalog read misses surge; PostgreSQL CPU hits 100%.
- 19:12 — IC declared incident SEV2.
- 19:17 — Emergency in-process cache added via feature flag.
- 19:29 — Redis cluster stabilizes, DB CPU drops, latency recovers.

## Root cause

When the Redis primary briefly disappeared during the replica failover,
the catalog service treated every lookup as a cache miss and hit the
database directly. Tens of thousands of concurrent requests all raced
to repopulate the same hot keys, thundering herd style.

## Contributing factors

- No request coalescing on cache miss: every in-flight request queried
  the database independently.
- The Redis failover procedure was not rehearsed in this service.
- Circuit breaker thresholds were tuned for latency, not for database
  CPU, so they did not trip during the stampede.

## Remediation

- Added singleflight around catalog cache misses so only one goroutine
  per key hits the database.
- Redis failover drill added to quarterly chaos engineering schedule.
- Circuit breaker now considers upstream DB CPU as a tripping signal.

# Postmortem: 2025-12-15 database lock contention

## Summary

On 2025-12-15 between 14:22 UTC and 14:58 UTC the API saw a p99 latency
increase from 180ms to 4.2s. Root cause was a migration statement that
acquired an exclusive lock on the `orders` table, blocking every read
and write for 36 minutes.

## Impact

- 36 minutes of severely degraded API performance.
- 42,000 requests returned `503 Service Unavailable` due to gateway
  timeout budgets being exceeded.
- No data loss.

## Timeline

- 14:20 — Migration job scheduled as part of the normal deploy.
- 14:22 — Alert fires: p99 latency above threshold.
- 14:28 — IC declared incident SEV2.
- 14:35 — DBA identifies blocking query as the migration.
- 14:47 — Migration aborted via the db migration abort runbook.
- 14:58 — Latency returns to baseline.

## Root cause

The migration added a new column with a default value to the `orders`
table. PostgreSQL rewrites the entire table for such migrations, which
requires an exclusive lock. The table is 180GB; the rewrite would have
taken roughly two hours.

## Contributing factors

- The migration review did not flag the non-NULL default as a rewriting
  operation.
- The migration job does not enforce a statement timeout.

## Remediation

- Migration review checklist updated to require explicit approval for
  any `ALTER TABLE ... ADD COLUMN ... DEFAULT ...` statement on tables
  larger than 1GB.
- Migrator job now enforces a `statement_timeout` of 5 minutes by default.
- Runbook for aborting migrations prominently linked from the deploy
  dashboard.

# Database Migration Abort Runbook

## Scope

This runbook covers aborting an in-flight schema migration on the primary
PostgreSQL cluster. It applies to online migrations run via the standard
migrator job, not to manual psql-driven changes.

## Signals that abort is necessary

- Migrator job has been running longer than its documented SLO.
- Replication lag on read replicas has grown beyond 60 seconds.
- Application error rate is climbing in correlation with migration progress.

## Abort procedure

1. Announce intent in `#database` and page the DBA on-call.
2. Pause the migrator job:

       kubectl -n data scale job/migrator --replicas=0

3. Inspect outstanding long-running queries:

       SELECT pid, state, query, query_start FROM pg_stat_activity
       WHERE state = 'active' ORDER BY query_start;

4. Cancel any migration-owned statements with `pg_cancel_backend(pid)`.
5. If a backend does not respond to cancel within 30 seconds, use
   `pg_terminate_backend(pid)` as a last resort.
6. Verify replication lag decreases once the long-running statements are gone.

## Post-abort

- Restore the migrator job only after the failing migration has been
  fixed or rewritten, and the fix has been reviewed by a second engineer.
- File an incident ticket describing what was aborted and why.
- Schedule a follow-up during a low-traffic window to retry the migration.

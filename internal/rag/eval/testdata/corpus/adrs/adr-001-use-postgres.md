# ADR 001: Use PostgreSQL as the primary relational store

## Status

Accepted

## Context

We need a relational database for the core application. Candidates were
PostgreSQL, MySQL, and CockroachDB. We evaluated them against our
requirements: ACID guarantees, mature tooling, extension ecosystem, and
operational familiarity in the team.

## Decision

Adopt PostgreSQL 15 as the primary relational datastore for all new
services. Existing services on MySQL will not be migrated unless an
independent business case is made.

## Consequences

- Strong consistency and well-understood transactional semantics.
- Rich ecosystem: logical replication, pg_stat_statements, pg_partman,
  pg_cron, TimescaleDB if we ever need it.
- Operational familiarity — the majority of the team has five or more
  years of PostgreSQL experience.
- Single-writer architecture requires care for horizontal scaling; we
  accept read replicas and partitioning as sufficient for the next
  three years of forecast load.
- CockroachDB was rejected because distributed SQL added operational
  complexity that outweighed the scalability gains at our current load.

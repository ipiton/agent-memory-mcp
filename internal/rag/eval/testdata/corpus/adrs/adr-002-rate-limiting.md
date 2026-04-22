# ADR 002: API rate limiting with a token bucket

## Status

Accepted

## Context

Public API clients occasionally generate traffic spikes that degrade the
experience for all users. We need a rate-limiting mechanism that:

- Protects upstream services from overload.
- Gives honest clients predictable limits they can design against.
- Does not require per-tenant infrastructure.

## Decision

Implement a token bucket algorithm in the API gateway, keyed by the
authenticated client ID. The default limit is 600 requests per minute
with a burst capacity of 60. Limits are configurable per tenant through
the admin API.

## Consequences

- Clients can recover from transient bursts without being blocked outright.
- Rejected requests receive `429 Too Many Requests` with a
  `Retry-After` header indicating when capacity will be available.
- Token counters are stored in Redis with a five-minute TTL so that
  short cluster outages do not penalize clients.
- A leaky bucket was rejected because it amortizes bursts less gracefully.
- Sliding window was rejected because of its higher memory cost.

## Monitoring

- Alert when any tenant sustains a 429 rate above 5% for more than 10
  minutes. That usually indicates either a misconfigured client or a need
  to raise that tenant's quota.

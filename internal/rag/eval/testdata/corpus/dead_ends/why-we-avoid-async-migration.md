# Dead End: async migration without dual-write

## Attempted approach

We tried to migrate the order-events pipeline from the legacy ingester to
the new async stream by flipping consumers over directly. The idea was
that once all readers pointed at the new stream, we could stop the old
ingester and call it done.

## Why it failed

The new stream had a different visibility model. During the cutover,
in-flight events already acknowledged by the old ingester had not yet
propagated to the new stream, while the new consumers had already moved
their read cursor forward. We lost roughly 1,200 order-events that never
reached downstream invoicing during a two-minute window.

The root cause was that async migration assumed eventual consistency at
the stream edge, but our consumer semantics required exactly-once
delivery. No matter how careful the cutover window was, we could not
close the gap without coordination between the two systems.

## Lesson learned

Async migration across two stream systems with different visibility
semantics is not safe without a dual-write phase. Even if the consumer
and producer traffic is healthy, in-flight messages can vanish when the
cutover happens in the seam between the two systems.

## Alternative that worked

We added a dual-write phase: the legacy ingester writes to both streams
for two weeks, while a shadow consumer on the new stream verifies
checksum and ordering match the old stream. Only once the shadow
consumer reports zero drift for seven straight days do we cut readers
over, and even then the legacy ingester keeps writing for one more week
as a rollback safety net.

## Avoid this pitfall by

- Always running a dual-write phase before any async migration.
- Treating acknowledgement semantics as a first-class migration risk.
- Using a shadow consumer with checksum parity as the go/no-go signal.

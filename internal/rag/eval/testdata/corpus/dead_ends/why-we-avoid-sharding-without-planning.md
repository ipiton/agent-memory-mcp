# Dead End: sharding ledger without capacity planning

## Attempted approach

To relieve hot-partition pressure on the ledger service we tried to
introduce horizontal sharding by a hash of account_id. The plan was to
pick a shard count that looked comfortable (32), rehash existing rows
online, and flip reads over once rehash finished.

## Why it failed

Capacity planning was skipped because we assumed 32 shards would more
than cover projected growth. In practice, 8% of accounts accounted for
roughly 70% of write throughput. Within 48 hours the top four shards
saturated disk I/O and the write queue backed up, forcing us to roll
back. A single enterprise tenant alone was hot enough to overwhelm one
shard on its own.

We also discovered that our rehash job had no backpressure: when the
target shards slowed down, the rehash pipeline kept accepting work from
the source and the queue grew unbounded.

## Lesson learned

You cannot reason about shard counts from account cardinality alone. The
distribution of traffic across accounts is what matters, and skew in
that distribution is the norm, not the exception. Picking a round number
that "feels right" is the biggest pitfall in sharding work.

## Alternative that worked

We backed out the naive hash sharding and replaced it with a
traffic-weighted shard map: each account is assigned to a shard based on
recent write rate, and a small set of "elephant" accounts each get a
dedicated shard. The rehash job now has explicit backpressure tied to
target-shard write latency.

## Avoid this pitfall by

- Measuring per-account write-rate distribution before picking any shard
  count.
- Treating skew as a first-class sharding input, not a corner case.
- Giving elephants their own shards instead of hoping hash collisions
  spread the load.
- Wiring backpressure into any online rehash pipeline from day one.

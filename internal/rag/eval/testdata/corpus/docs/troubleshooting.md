# Troubleshooting Guide

## General approach

1. Start in Grafana at the service overview dashboard.
2. Check error rate, latency, and saturation panels first.
3. Correlate spikes with recent deploys visible on the deploy annotations
   layer.
4. If a deploy-correlated regression is clear, rollback first and
   investigate second.

## Common symptoms

### Elevated 5xx response rate

- Check database CPU and connection count. A saturated database shows
  up as 5xx with latency spikes.
- Look at recent traces for the failing endpoint in Tempo. Find the
  slowest span; that is usually the suspect.
- Consider cache health. See the cache stampede postmortem for a
  historical example of what this looks like.

### Elevated latency, no errors

- Inspect Redis hit rate. If cache hit rate drops, investigate whether
  a deploy changed the cache key shape.
- Check for long-running database queries:
  `SELECT pid, state, query FROM pg_stat_activity WHERE state = 'active'`.

### TLS handshake failures

- Check certificate validity via the TLS renewal runbook.
- Look for expiry dates in the near future; cert-manager may have
  failed to renew silently.

### Pods OOM-killed

- Check goroutine count in the service panel. Uncontrolled goroutine
  growth has been the root cause of every OOM in the last year.
- Review the memory leak postmortem for the canonical pattern.

## When to escalate

- SEV1/SEV2 incidents: follow the incident response runbook.
- Unclear root cause after 30 minutes: page a senior engineer.
- Suspected security impact: page the security on-call immediately.

# Postmortem: 2025-11-03 TLS certificate expiry

## Summary

On 2025-11-03 at 08:14 UTC the public API started returning TLS handshake
errors. The certificate serving `api.example.com` had expired seven
hours earlier. Automatic renewal via cert-manager had been failing
silently for the previous 28 days.

## Impact

- 47 minutes of total unavailability for all API consumers using HTTPS.
- One major enterprise customer failed a batch ingestion job.
- No data loss.

## Timeline

- 01:01 UTC — Certificate expires. Gradual rollout in client caches.
- 08:14 UTC — First customer report; pager fires.
- 08:21 UTC — On-call engineer diagnoses expired certificate.
- 08:48 UTC — Manual renewal via cert-manager force annotation succeeds.
- 09:01 UTC — All traffic recovers.

## Root cause

The ACME HTTP-01 challenge had been failing since the previous ingress
controller upgrade. The cert-manager Order objects reported `OrderFailed`
but the alert on certificate age had been suppressed during a planned
maintenance window and was never re-enabled.

## Contributing factors

- Suppressed alerts were not on a time-boxed silence.
- No dashboard surfaced cert-manager errors as a first-class signal.
- The ingress upgrade runbook did not include "verify ACME HTTP-01
  challenge succeeds" as a post-change check.

## Remediation

- All alert silences must now carry a mandatory expiry.
- Added the TLS renewal runbook and linked it from the incident dashboard.
- Added a CI check that flags ingress upgrades without certificate
  verification steps.
- Synthetic check added that validates every public certificate daily
  and pages two weeks before expiry.

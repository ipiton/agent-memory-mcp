# Postmortem: 2025-08-17 config drift caused feature outage

## Summary

On 2025-08-17 the recommendation service returned empty results for
three hours. Root cause was a Helm values override applied manually
during an emergency hotfix that was never reconciled back into git.
The override silently disabled the upstream model endpoint.

## Impact

- 3 hours of empty recommendation widgets on the landing page.
- Estimated revenue impact of $12k based on average conversion lift
  from recommendations.
- No data loss.

## Timeline

- Day -9 — Emergency kubectl edit to unblock a different deploy. Change
  sets `modelEndpoint: ""` as a temporary workaround.
- Day 0, 06:00 — Normal CI deploy overwrites the ConfigMap with the
  values from git, which still lacked the fix the emergency had added.
- Day 0, 06:03 — Recommendation responses start returning empty.
- Day 0, 09:14 — Product team reports the issue.
- Day 0, 09:31 — On-call traces the empty results to the missing endpoint.
- Day 0, 09:58 — Fix committed to git, deployed, recommendations restored.

## Root cause

The manual hotfix was never committed back to the git source of truth.
The next automated deploy drifted the cluster state back to the broken
configuration from git, but nothing alerted because the service still
reported healthy.

## Contributing factors

- No reconciliation check between the live Helm release and the git
  source of truth.
- The recommendation service's health probe validated that the process
  was running, not that results were non-empty.

## Remediation

- Argo CD deployed to continuously reconcile git to cluster state.
- Alert added on zero-result rate for recommendation responses.
- Runbook written for emergency hotfixes explicitly requiring a
  follow-up commit before the on-call shift ends.

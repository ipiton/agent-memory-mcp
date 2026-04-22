# Deploy Rollback Runbook

## When to use

Use this runbook when a production deployment must be rolled back due to
elevated error rates, failed health checks, or customer-visible regressions
within the 30-minute post-deploy window.

## Preconditions

- Previous stable image tag is available in the container registry.
- You have kubectl access to the production cluster.
- Slack channel `#incidents` is ready for announcements.

## Rollback steps

1. Announce in `#incidents` that a rollback is about to start and link to
   the current deploy PR.
2. Identify the previous stable tag:

       kubectl -n prod get deployment api -o jsonpath='{.metadata.annotations.previous-image}'

3. Roll back the deployment to the previous stable image:

       kubectl -n prod rollout undo deployment/api

4. Watch the rollout until the new replicas are ready:

       kubectl -n prod rollout status deployment/api --timeout=5m

5. Verify error rate returns to baseline in the monitoring dashboard.
6. Post a confirmation message in `#incidents` that the rollback completed.

## Follow-up

- Open a post-incident ticket tagged `rollback` to track root-cause analysis.
- Do not attempt another deploy until the regression is understood and fixed.
- If the rollback itself fails, escalate to the on-call platform engineer.

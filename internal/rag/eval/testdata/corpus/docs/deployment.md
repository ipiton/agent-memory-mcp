# Deployment Guide

## Pipeline overview

1. PR merged to `main`.
2. GitHub Actions runs the build and test workflow.
3. Docker image is built, scanned for CVEs, and pushed to the registry.
4. A bot opens a PR updating the image tag in the deployment repo.
5. After automerge, Argo CD reconciles the change to the cluster.
6. Health checks and post-deploy smoke tests run.
7. Slack notification in `#deploys` confirms success.

## Rollback

For rollback procedures see the deploy rollback runbook. The short form:

    kubectl -n prod rollout undo deployment/<service>

## Environments

- `dev`: ephemeral per-branch environments, torn down after merge.
- `staging`: shared, tracks `main` within 10 minutes of merge.
- `prod`: production traffic, deployments are manual-approval only.

## Approval gates

- `prod` deploys require one approval from the service's code owners.
- Any change touching database migrations requires DBA approval on top.
- Any change touching authentication requires security team approval.

## Post-deploy checklist

- [ ] Error rate within 10% of pre-deploy baseline for 15 minutes.
- [ ] p99 latency within 15% of pre-deploy baseline for 15 minutes.
- [ ] No new pages for this service.
- [ ] Smoke tests green.

If any of the above fails, consider rollback per the runbook.

# Incident Response Runbook

## Purpose

Operational runbook for declaring, triaging, and coordinating production
incidents. Follow this whenever a customer-facing SLO is at risk.

## Severity levels

- SEV1: Major outage affecting the majority of users.
- SEV2: Partial outage or significant degradation.
- SEV3: Minor degradation with a workaround in place.

## First five minutes

1. Declare the incident in `#incidents` with severity and a one-line summary.
2. Assign an Incident Commander (IC) and a Communications lead.
3. Create a dedicated Zoom bridge for the response.
4. Start an incident timeline document. Record every action with timestamp.
5. Page the relevant service owners via the on-call rotation.

## During the incident

- The IC coordinates; engineers execute.
- Post status updates every 15 minutes, even if there is nothing new.
- Prefer rollback over forward-fix when the regression was introduced
  within the last 60 minutes. See the deploy rollback runbook.
- If secrets were potentially exposed, trigger the secret rotation runbook
  in parallel. Do not delay rotation waiting for root cause.

## Resolution

- Declare the incident resolved only when SLOs are back to baseline.
- Send a final status update summarizing impact and next steps.
- Schedule a postmortem within 5 business days.

## Escalation

If the on-call engineer cannot reach an owner within 15 minutes, escalate
to the head of the owning team. If the head is unreachable, escalate to
the VP of Engineering.

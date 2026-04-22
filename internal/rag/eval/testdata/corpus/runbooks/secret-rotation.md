# Secret Rotation Runbook

## When to rotate

Rotate a secret whenever one of the following holds:

- The secret has been exposed in a log, commit, screenshot, or ticket.
- A team member with access has left the organization.
- The secret is approaching its scheduled rotation deadline.
- An incident response requires precautionary rotation.

## Rotation procedure

1. Announce in `#security` which secret is being rotated.
2. Generate a new value in the authoritative provider (Vault, cloud KMS,
   the SaaS provider's console).
3. Store the new value in Vault at the same path used by the workloads.
4. Trigger External Secrets Operator sync so Kubernetes secrets update:

       kubectl -n external-secrets rollout restart deployment/external-secrets

5. Restart consuming workloads to pick up the new secret:

       kubectl -n <ns> rollout restart deployment/<app>

6. Revoke the old value only after confirming all consumers have picked up
   the new one (watch pod logs for auth errors for at least one hour).

## Verification

- New auth attempts succeed with the rotated credential.
- Old credential is rejected (test from a disposable environment).
- Vault audit log shows the rotation event with the expected actor.

## Common pitfalls

- Do not rotate in place without announcing — other teams may be mid-deploy.
- Never paste the new secret into chat. Share only the Vault path.
- If the rotation involves a database user, coordinate with the DBA on-call
  to avoid breaking long-running connections.

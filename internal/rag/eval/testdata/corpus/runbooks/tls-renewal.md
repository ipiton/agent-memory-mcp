# TLS Certificate Renewal Runbook

## Overview

All public-facing TLS certificates are issued by cert-manager using the
Let's Encrypt HTTP-01 challenge. This runbook covers the flow when
automatic renewal fails and manual intervention is required.

## Preconditions

- You have kubectl access to the cluster hosting the ingress controller.
- The domain's DNS is still pointing at the cluster's load balancer.

## Diagnosis

1. Inspect the Certificate resource:

       kubectl -n <ns> describe certificate <name>

2. Look for the `Ready` condition. If `False`, read the `Reason` and
   the most recent `Events` at the bottom.
3. Common failure modes:
   - `OrderFailed` — ACME order rejected, usually a DNS/HTTP challenge issue.
   - `SecretNotFound` — the target secret was manually deleted.
   - `RateLimited` — Let's Encrypt weekly quota exceeded.

## Manual renewal

1. Force re-issuance by annotating the Certificate:

       kubectl -n <ns> annotate certificate <name> \
         cert-manager.io/force-renew="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

2. Watch the challenge pods complete:

       kubectl -n <ns> get challenges --watch

3. Once the Certificate reports `Ready=True`, confirm the secret has a
   new NotAfter date:

       kubectl -n <ns> get secret <tls-secret> -o jsonpath='{.data.tls\.crt}' \
         | base64 -d | openssl x509 -noout -enddate

## Escalation

If rate-limited, switch the Issuer temporarily to the Let's Encrypt staging
environment until the weekly quota window rolls over, then switch back.
For anything else, page the platform on-call.

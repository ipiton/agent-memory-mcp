# ADR 004: Authentication provider selection

## Status

Accepted

## Context

We need a hosted identity provider that supports OIDC for internal tooling
and workforce SSO. Candidates evaluated: Auth0, Okta, Keycloak (self-hosted),
and Zitadel (self-hosted).

## Decision

Adopt Zitadel as the primary identity provider, deployed into our
Kubernetes cluster. Issue OIDC tokens for every internal tool and for
the B2B customer-facing portal.

## Consequences

- Self-hosted cost profile is significantly lower than Auth0 or Okta
  at our projected user count of 10k+ internal and external identities.
- Zitadel's multi-tenant project model maps cleanly to our customer
  organizations.
- Operational burden increases: we now run PostgreSQL and Zitadel with
  99.9% availability. The platform team accepts this cost.
- Keycloak was rejected because its operational tooling lags Zitadel's,
  particularly around upgrades and config-as-code.

## Migration plan

Migration from the legacy internal auth service will be phased over three
quarters. Each internal tool cuts over independently once tested. The
legacy service is decommissioned only after the last consumer migrates.

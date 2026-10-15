# Global inference limits

Open **Settings → Router settings → Global rate limits** to inspect and configure
the aggregate requests and tokens admitted per minute across all identities,
teams, organizations and models. Only a global administrator can read or change
these settings. Management and health requests do not consume this inference
quota. Existing credential, provider and deployment quotas remain independent.

The startup defaults are `GLOBAL_RATE_LIMIT_RPM=60` and
`GLOBAL_RATE_LIMIT_TPM=100000`. Helm exposes them as
`gateway.globalRateLimits.rpm` and `gateway.globalRateLimits.tpm`. Zero disables
the corresponding dimension; negative, fractional and out-of-range values are
rejected at startup or by the management API. This changes the previous startup
behavior, which did not enforce an aggregate inference quota. Set both to zero
to retain that behavior.

A UI override is persisted through the existing admin-state control plane and
takes precedence over Helm / ENV. **Use Helm / ENV defaults** removes the override.
An older configuration snapshot without these settings uses the current startup
defaults. Failed persistence restores the previous settings, and other replicas
refresh persisted settings before admission. With no PostgreSQL control plane,
UI configuration is process-local and is lost on restart.

With Redis, a shared global counter enforces the aggregate quota across replicas.
Without Redis each process has an independent counter: use one replica for a
global memory quota. All replicas must use the same Redis namespace and startup
defaults. Counters use fixed one-minute windows, not a rolling minute; changing
settings does not reset an active window. A blocked request returns HTTP 429 and
`Retry-After`. Observed storage/configuration errors fail closed with HTTP 503.
Replica configuration uses the existing
`PROVIDER_CONTROL_PLANE_REFRESH_SECONDS` cadence (one second by default); a saved
change can take that interval to reach another replica. Redis counter failures
are detected on each admission, independently of configuration refresh.

TPM reserves the estimated input context plus the maximum output allowance. A
Docling deployment converts inline PDFs before admission, so encoded transport
bytes are not model tokens. Reservation is conservative and is not reconciled
to actual usage or refunded on provider failures. Credential admission precedes
global admission: a request rejected by the global quota can still consume its
credential reservation. This prevents retries from bypassing the tighter quota.
Each executed batch item and each admitted Realtime response also use this path.

The additive management API is `GET`, `PUT`, and `DELETE`
`/admin/v1/settings/global-rate-limits`. PUT requires both integer fields:
`{"rpm":60,"tpm":100000}`. GET returns effective `limits`, startup `defaults`,
and `source` (`environment` or `configuration`). Mutations are audited through
the existing administrative audit path.

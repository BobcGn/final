# Deploy

Integration environment for the acceptance run.

| File | Purpose |
| --- | --- |
| `compose.yaml` | EMQX for local integration runs; PostgreSQL reuses the existing `postgres-dev` container |
| `emqx/acl.conf` | Broker authorization rules implementing the direction split the device contract requires |

**Nothing here is a production deployment.** The Broker credentials are local
examples. The database password remains in the existing `postgres-dev` container;
do not commit it. Initialize `lab` with `../backend/database/bootstrap.sql` as
described in `../backend/README.md` before running the integration scenario.

The EMQX 5.8 container and file ACL were exercised during the 2026-09-21 hardware
acceptance run. The device and backend connected with separate usernames and the
real telemetry route was accepted. This is local integration evidence, not a
production security review; TLS and per-device credentials remain deployment work.

Startup instructions are in [`../docs/local-runbook.md`](../docs/local-runbook.md),
and the detailed acceptance matrix is in
[`../docs/integration-testing.md`](../docs/integration-testing.md).

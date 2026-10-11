# CDN production completion

Release: server v0.1.100 / web v0.1.134.

CDN purchase and overage bills reuse platform orders and BalanceService. CDN amounts are yuan decimal; platform order amounts remain integer cents. Delivery checks original totals, then uses the durable payment coordinator. Configuration and certificate tasks report success only after verified cloud readback. Domain removal requires offline state and explicit absence confirmation; uncertain requests are not repeated. Usage corrections are idempotent and allocate FIFO. Policy restoration applies only to pauses owned by this module. Notifications merge per customer/channel/day.

## Deployment

Use existing online GitHub update. Migrations 040–045 must be applied separately by the local DBA using the binary produced by the online isolated build, `-migrate -migration-socket`, then grant narrowly scoped privileges on the three new usage tables. Build identities receive no database credentials or write grants. Do not import fixture data or copy local databases/configuration.

Legacy rollback is allowed only for a disabled, empty CDN module protected by migration 045 and an active unique backend update task. Nonempty modules require a rollback target declaring `ddyidc:cdn-yuan-v1`. Never downgrade persisted money units. The bootstrap guard persists after failed updates until a later successful backend update.

KS3 logging is optional and requires account configuration. Do not open KS3 automatically. Official download results can later be retained locally; this does not remove the provider's KS3 prerequisite.

## Acceptance

VM: Go build/vet/test; actual MySQL concurrency/rollback/permissions; real browser login, platform balance payment, durable delivery; desktop/mobile light/dark layout; legacy binary recovery with unchanged business fingerprint. Authorized cloud domain create/config/TLS/refresh/preload/start/stop/removal tested and resources cleaned. Live tests and browser fixtures are opt-in `_test.go` files, not application startup logic. No real customer data or secrets are included.
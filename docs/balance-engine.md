# Customer balance engine

## Storage and compatibility

`internal/billing/balance.BalanceService.Change` is the money mutation entry point. `Money` uses integer cents in memory and a decimal yuan string for SQL; floating-point inputs, scientific notation and excess precision are rejected. The existing `billing.accounts` and `billing.transactions` are migrated in place. `customer_id`, `tx_no`, `related_type`, `related_id`, `balance_before`, `balance_after` and `remark` retain their established names. `tx_no` is the globally unique idempotency key; a request fingerprint rejects changed payloads.

The seller settlement wallet is an existing, separate business account. `Scope=marketplace_wallet` routes the same Change entry point to `system.marketplace_wallet` and `system.marketplace_flows`; it preserves settlement freezes, fees, withdrawals and their existing separation from spendable customer credit. Its flows gain snapshots, actor fields and a version. Informational buyer activity flows are not account mutations.

Existing frontend/order/points/groupbuy contracts that use integer cents still do so. Conversion occurs at the billing storage boundary. The new clients credit API returns exact decimal strings. The old admin read endpoints convert back to cents explicitly.

## Mutation chain

1. Required command fields and actor/reason validation.
2. Exact decimal and operation-sign whitelist checks; HTTP manual requests accept only positive amounts and the server chooses the sign.
3. Unique idempotency key and payload fingerprint; batch unfreeze fingerprints the entire batch.
4. Business owner, amount and lifecycle-state checks. Payment recharge must be paid in the enclosing transaction.
5. Account row lock and available/frozen sufficiency checks.
6. Append snapshot ledger, then version-conditional update, in one transaction/savepoint. An outer business rollback also rolls back money.

Manual recharge creates the existing recharge order and payment records inside the same transaction as crediting. A failed operation rolls back the order, payment and credit together; it never leaves a paid order without funds.

Changes exceeding CNY 50,000 require frontend confirmation and `system.system_configs` key `billing.large_adjustment.admin.<admin_id>` with value `1`. Only grant this flag through the existing privileged configuration process. It is not accepted from HTTP as a permission. Five manual operations per customer per minute emit `BILLING_ALERT`.

## Offline migration

Stop the application and all balance workers. Retain all backups outside Git with restrictive permissions.

```
python3 scripts/billing/migrate_balance.py --config etc/config.yaml --backup-dir /secure/backups
python3 scripts/billing/migrate_wallet.py --config etc/config.yaml --backup-dir /secure/backups
mysql < migrations/20260913_customer_audit.sql
```

The billing migration dumps all billing tables, validates historical ledger signs and reconciliation, widens columns before dividing cents by 100, maps numeric transaction types, and reconciles again. Historical marketplace type-4 inbound refunds are mapped to `refund`, not withdrawal. Unknown or inconsistent histories are rejected, never repaired with invented opening funds. Nonzero legacy freezes without identifiable freeze records are rejected for manual investigation.

A failure after DDL restores the complete backup. A marker supports recovery after process interruption. Re-running a completed migration verifies reconciliation without dividing a second time. The seller wallet migration backs up its existing wallet/flow tables and verifies that monetary values and row counts remain unchanged.

Start the new application only after both migrations and reconciliation succeed. Daily checks compare billing balance to signed ledger sums and frozen balance to outstanding freeze rows; missing accounts also alert. Audited seller wallet versions and snapshot deltas are checked separately, preserving any first audited opening snapshot of the pre-existing wallet.

## Guard and notifications

`sql/20260913_balance_guard.sql` is production opt-in only. `balance_guard=off` means do not install it; the VM tests do not install triggers. The account update guard requires a matching newly appended ledger/version. A DBA session can use `@balance_admin_reason` for a documented correction, which is automatically recorded in `billing.balance_guard_audit`. Transaction update/delete triggers enforce append-only storage. Restrict application DDL and audit-table privileges; the application does not depend on these optional triggers.

Freeze notifications are enqueued atomically in `billing.notification_outbox`. The existing notification manager sends the amount/reason using customer contact preferences. Failure stays pending for retry and emits an alert; missing delivery credentials are not treated as a successful external send. Configure existing email/SMS transports for delivery. Notification transport acceptance is distinct from final recipient delivery.

## APIs and authentication

- GET/PUT `/admin/v1/clients/:id`
- POST `/admin/v1/clients/:id/recharge`
- PUT/GET `/admin/v1/clients/:id/credit`
- POST/GET `/admin/v1/clients/:id/credit/freeze`
- POST `/admin/v1/clients/:id/credit/unfreeze`
- POST `/admin/v1/clients/:id/login`

All endpoints require the existing administrator session. Manual commands require `request_id` (or `Idempotency-Key`) and `notes`. User impersonation issues the existing Redis-backed user JWT, records both audits, and sets the role-specific httpOnly cookie. The frontend verifies the returned JWT with an Authorization Bearer request before navigating; it does not persist the token in localStorage or the URL. Profile auditing stores changed field names, never passwords or password hashes.

## Verification

On the Linux VM:

```
go build ./...
go vet ./...
go test ./...
DDYIDC_BALANCE_INTEGRATION=1 DDYIDC_GROUPBUY_INTEGRATION=1 go test -v ./internal/marketplace ./internal/groupbuy
```

The MySQL lifecycle test covers purchase, pending and settled refunds, settlement, withdrawal, recharge completion/replay, points-to-balance conversion and reconciliation. Its marketplace fixtures and configuration changes are rolled back. Groupbuy lifecycle fixtures are isolated and cleaned up. Real HTTP tests cover missing fields, negative amounts, concurrent debits, freeze/unfreeze replay, changed batch payloads, batch rollback, profile persistence and impersonation. VM-only evidence and migration fault-injection results are recorded in the delivery report, not as credentials in this repository.

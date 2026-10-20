# Renewal original-price payment gate

Status: local candidate, not released. This change does not alter paid orders, balances or supplier machines. The local integration branch includes the previously developed message center; production remains unchanged.

## Payment and price contract

`internal/shared/renewalquote` stores immutable price revisions and rechecks them inside the payment transaction. `internal/instance/service/renewal_quote_gate.go` performs supplier reads before taking locks. Explicit full original retail and procurement cost stay separate; another billing cycle's discount is never used to derive a price. Fixed non-configurable current-cycle catalogue verification bypasses the display cache and requires an explicit matching product ID.

A proof binds customer, instance, ownership, local product, supplier/remote identity, configuration hash, cycle/duration, base expiry, exchange rate, gross original price, cost and rule version. Order gross, coupon discount, payable net and the sole renewal item must agree. A proof expires after five minutes. Remote I/O never runs while the payment row locks are held.

Balance renewal, the generic order-balance delegation, and online order checkout share this gate. Missing/mismatched evidence fails closed. A real externally captured receipt remains durable; unverified or expired legacy checkout receipts enter `received_review`, retaining the existing original-channel refund review path. The callback never recalculates an already captured amount. Fulfillment independently refuses an unverified legacy price before creating/paying a supplier invoice.

## API changes

- Existing GET renewal-options: each item adds `quoteRevision`.
- Existing POST instance renewals: requires `cycle` and `quoteRevision`; optional `replaceOrderId` remains compatible with older clients; current clients need no replacement confirmation.
- Existing PUT auto-renew: enabling requires `enabled=true` and a current server `quoteRevision`, fetched internally by the existing toggle without an extra dialog. Disabling remains permitted without a quote.
- Existing renewal/order checkout and pay APIs: quote errors use HTTP409 and data.code (`RENEWAL_PRICE_CHANGED`, `RENEWAL_QUOTE_EXPIRED`, `RENEWAL_QUOTE_UNVERIFIED`, `RENEWAL_PAYMENT_RECONCILING`). Instance and payment handlers return the structured old/new amounts and old order reference where available. Generic order-balance wrapper retains its existing error envelope/message.
- New GET `/admin/v1/instances/renewal-quote-audit?page=1`: existing instance-list admin permission and customer scope; page size20; local snapshot inventory only, not live price authorization and not a bulk correction API.

Following the user policy update on2026-10-20, a verified unpaid renewal without any channel transaction is corrected in place under locks and a yuan-denominated before/after audit is appended. The order ID stays unchanged. The already-reserved coupon discount remains unchanged; correction does not release/redeem it or move funded-coupon balances. No price-change comparison or rebuild confirmation is shown. A cycle change can use the existing safe cancellation/replacement API without an extra price-confirmation step. Retries reuse the effective order. Any historical channel payment blocks automatic replacement: a locally closed flag is not evidence of remote closure. Already paid/unknown supplier outcomes never use the replacement path.

## Auto renewal

An explicitly approved quote is stored per instance and ownership version. The maintenance job compares current price/cycle/binding before creating or paying; missing legacy approval or changed pricing pauses auto renewal without a debit. A stale job cannot override a newer approval. Reasons persist as deduplicated pending alerts and appear in instance details. The same transaction enqueues a durable billing warning through the message module. Its publisher sends an SSE change hint and the client uses the existing Kumo Toast. Critical warnings remain pending while delivery is disabled; duplicate event keys are idempotent. Delivery checks the instance owner and ownership version again. The instance alert remains an audit fact, not proof of inbox delivery or human receipt.

## Schema and rollout

New immutable migration `053_renewal_quote_evidence.sql`, registered in `tools/architecture/schema-checksums.json`, adds four instance-owned tables: quote evidence, checkout references, auto-quote approvals, pending alerts. No existing money columns are rewritten. The existing offline socket migration grants read/append only for immutable proofs and alerts; auto approval updates are restricted to its six mutable columns. Runtime receives no DDL or proof rewrite/delete rights. Migrations050–052 belong to the integrated message center.

Ship backend and frontend together: old clients lacking quote revisions are rejected and must refresh. Use the existing official offline migration and online updater; do not manually update financial rows. Rolling back to a version without the payment gate is unsafe for legacy wrong-price orders. Existing channel receipts can still arrive and must be recorded/reviewed even when a payment is blocked.

## Verification

Use `deploy/linux/tests/test_renewal_quote_gate.sh` against a dedicated disposable MySQL socket. It applies official migrations and runs real transaction tests, explicit price/cost fixtures, coupon-net validation, permission scope, expiry/rate/ownership races, duplicate callbacks, paid legacy fulfillment rejection, safe replacement and auto-renew consent. The finance recovery CI job includes these cases. Browser evidence uses the real frontend component with isolated in-memory responses; it cannot pay and is not presented as live supplier fulfillment.

Detailed dated reports remain in deploy-notes and the Obsidian development map. No private customer identifiers or credentials belong in repository docs.

## Admin inventory and concurrent reads

The admin product navigation links to `/instances/renewal-quote-audit`. It shows the stored evidence source, verification and expiration times, local snapshot and review hint, with manual refresh and twenty-row pagination. These are historical observations, never payment authorization.

Overlapping renewal-option reads share work only within one customer and instance. The leader remains inside the admitted HTTP request; no detached financial goroutine is added. A cancelled waiter does not cancel the leader. Surviving waiters can retry when a cancelled leader exits. No completed quote is cached, and the actual payment transaction still validates its immutable binding.

Evidence validity defaults to five minutes. The existing system configuration key `instance.renewal.quote_ttl_seconds` accepts integer seconds30–900; an invalid explicit value blocks payment instead of silently extending validity. It is an operational setting, with no new client cache or polling. The original channel shutdown remains conservatively manual whenever any channel payment exists. Actual supplier fulfillment and historical compensation are outside local fixture verification.

## Route coverage

| Entry | Gate |
|---|---|
| GET `/api/v1/instance/:instanceId/renewal-options` and admin customer-instance equivalent | Explicit current original, quote revision |
| POST same base `/renewals` | Current revision, safe prior-order reuse/replacement |
| POST same base `/renewals/pay` | Live preflight and transaction evidence before BalanceService.Change |
| PUT same base `/auto-renew` | Explicit enable consent; scheduler rechecks before order/debit |
| POST `/api/v1/orders/:id/pay` with balance | Order service delegates renewal payments to the same instance gate |
| POST `/api/v1/orders/:id/pay` with channel, or `/api/v1/payment/order/:orderId/create` | Payment service preflight, repository transaction check and exact checkout binding |
| POST `/api/v1/payment/notify/wechat` and `/notify/alipay` | Real signed receipt preserved; incompatible proof enters received_review |
| Renewal durable task and reconciliation | Existing invoice/unknown fences retained; no unaudited legacy execution |

The admin customer-instance equivalent base is `/admin/v1/customers/:customerId/instances/:instanceId`. Source routes are `internal/instance/handler/management.go`, `internal/order/handler/handler.go` and `internal/payment/handler/handler.go`. There is no newly added bypass or admin forced-price payment endpoint.

##2026-10-20 user flow adjustment

The ordinary renewal/payment flow is restored. InstanceRenewal fetches a current revision internally and shows only the actual order amount. CorrectUnpaidRenewalPriceTx is the order module owner API; instance rows and price evidence remain instance-owned. Existing paid orders, payment attempts, supplier invoice/unknown states and malformed discount records are not repriced. Price correction appends system audit amounts in yuan, without any balance write. Payment checkout responses now include amount (yuan string), so QR/checkout labels use the gateway's actual amount rather than a stale page prop. Source: internal/order/service/renewal_price_correction.go, internal/instance/service/renewal_price_correction.go, payment/types and frontendPaymentDialog.

TestRenewalQuoteCorrectionMySQL exercises same-order31.77→30 correction, idempotent audit, exact30 gateway amount, refusal after a checkout exists, and preserving a one-yuan coupon without hook/ledger movements. This supersedes the earlier extra confirmation UX, not the receipt/locking protections.

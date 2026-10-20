# Renewal original-price payment gate

Status: local candidate, not released. This change does not alter paid orders, balances or supplier machines. Message-center realtime delivery is a separate unmerged dependency; no claim of delivered alerts is made.

## Payment and price contract

`internal/shared/renewalquote` stores immutable price revisions and rechecks them inside the payment transaction. `internal/instance/service/renewal_quote_gate.go` performs supplier reads before taking locks. Explicit full original retail and procurement cost stay separate; another billing cycle's discount is never used to derive a price. Fixed non-configurable current-cycle catalogue verification bypasses the display cache and requires an explicit matching product ID.

A proof binds customer, instance, ownership, local product, supplier/remote identity, configuration hash, cycle/duration, base expiry, exchange rate, gross original price, cost and rule version. Order gross, coupon discount, payable net and the sole renewal item must agree. A proof expires after five minutes. Remote I/O never runs while the payment row locks are held.

Balance renewal, the generic order-balance delegation, and online order checkout share this gate. Missing/mismatched evidence fails closed. A real externally captured receipt remains durable; unverified or expired legacy checkout receipts enter `received_review`, retaining the existing original-channel refund review path. The callback never recalculates an already captured amount. Fulfillment independently refuses an unverified legacy price before creating/paying a supplier invoice.

## API changes

- Existing GET renewal-options: each item adds `quoteRevision`.
- Existing POST instance renewals: requires `cycle` and `quoteRevision`; optional `replaceOrderId` is explicit consent to replace the displayed old order.
- Existing PUT auto-renew: enabling requires `enabled=true` and the displayed `quoteRevision`. Disabling remains permitted without a quote.
- Existing renewal/order checkout and pay APIs: quote errors use HTTP409 and data.code (`RENEWAL_PRICE_CHANGED`, `RENEWAL_QUOTE_EXPIRED`, `RENEWAL_QUOTE_UNVERIFIED`, `RENEWAL_PAYMENT_RECONCILING`). Instance and payment handlers return the structured old/new amounts and old order reference where available. Generic order-balance wrapper retains its existing error envelope/message.
- New GET `/admin/v1/instances/renewal-quote-audit?page=1`: existing instance-list admin permission and customer scope; page size20; local snapshot inventory only, not live price authorization and not a bulk correction API.

An unpaid wrong-price order is not silently overwritten. Without confirmed consent the original is retained. The safe replacement transaction uses the order module cancellation and coupon-release hooks, preserves the old amount, cancels the renewal intention, creates the replacement and stores `replacesOrderId`. Retries return the same current order. Any historical channel payment blocks automatic replacement: a locally closed flag is not evidence of remote closure. Already paid/unknown supplier outcomes never use the replacement path.

## Auto renewal

An explicitly approved quote is stored per instance and ownership version. The maintenance job compares current price/cycle/binding before creating or paying; missing legacy approval or changed pricing pauses auto renewal without a debit. A stale job cannot override a newer approval. Reasons persist as deduplicated pending alerts and appear in instance details. Realtime inbox/Toast delivery is NOT wired in this candidate because the message-center branch is not in this main baseline; pending alerts must not be labelled sent. Do not publish the complete task as finished until that dependency is integrated and verified.

## Schema and rollout

New immutable migration `053_renewal_quote_evidence.sql`, registered in `tools/architecture/schema-checksums.json`, adds four instance-owned tables: quote evidence, checkout references, auto-quote approvals, pending alerts. No existing money columns are rewritten. The existing offline socket migration grants read/append only for immutable proofs and alerts; auto approval updates are restricted to its six mutable columns. Runtime receives no DDL or proof rewrite/delete rights. Numbers050–052 are reserved for the separately developed message-center migration.

Ship backend and frontend together: old clients lacking quote revisions are rejected and must refresh. Use the existing official offline migration and online updater; do not manually update financial rows. Rolling back to a version without the payment gate is unsafe for legacy wrong-price orders. Existing channel receipts can still arrive and must be recorded/reviewed even when a payment is blocked.

## Verification

Use `deploy/linux/tests/test_renewal_quote_gate.sh` against a dedicated disposable MySQL socket. It applies official migrations and runs real transaction tests, explicit price/cost fixtures, coupon-net validation, permission scope, expiry/rate/ownership races, duplicate callbacks, paid legacy fulfillment rejection, safe replacement and auto-renew consent. The finance recovery CI job includes these cases. Browser evidence uses the real frontend component with isolated in-memory responses; it cannot pay and is not presented as live supplier fulfillment.

Detailed dated reports remain in deploy-notes and the Obsidian development map. No private customer identifiers or credentials belong in repository docs.

# Independent payable billing

`orders` owns the purchase and delivery facts. This module owns payable
documents, immutable line snapshots, revisions and settlement associations.
It is an application module in the existing project, not another gateway or
balance implementation.

## Integration contract

- Other domains import `internal/billing/api` only. New products/modules create
  a platform order with their module type and use the existing payment gateway.
- After the order, items and final discounts are saved, call
  `billing.IssueOrderTx(tx, orderID)` **inside the same caller-owned transaction**.
  Do not call transaction APIs with a standalone/autocommit database handle.
- Register the original product balance payer with the existing order service.
  The composition root injects payment adapters into `billing.NewApplication`.
  A new product using platform orders needs no copied billing price calculator.
- Customer checkout uses the invoice ID. The application pins that ID into the
  original payment transaction; a price revision cannot silently charge a
  replacement document. Legacy payment URLs run the same invoice checks.
- Original verified receipt/balance acceptance calls the platform order state
  owner. Its invoice acceptance is in the same transaction as the business,
  ledger and durable payment intention. Invoice APIs do not prove a payment and
  must never replace signature verification or `BalanceService.Change()`.
- Approved refunds retain the original review calculation and money writer.
  Billing records the proven application amount and idempotent event; it does
  not calculate or pay a second refund. Unknown historical cumulative partial
  refunds stay unknown rather than being invented.

## Storage and compatibility

- The payable entity is `billing.payable_invoices`. Existing `billing.invoices`
  contains tax invoices and is deliberately preserved.
- Amounts at the invoice database/HTTP boundary are exact decimal CNY yuan;
  `balance.Money` uses integer cents internally. Original order/payment units
  are converted by source adapters, not changed in place.
- `invoice_bindings` has one current binding per business source. Legacy
  pre-delivery group payment records and their later delivery orders can point
  to the same paid invoice; this does not create a second receivable.
- Legacy standalone marketplace/group records have explicit compatibility
  adapters. Do not copy those legacy payment patterns into new modules.
- Versions, source records, payment numbers, receipts, ownership and rewards
  are retained. Read endpoints never issue a bill or change financial records.
- An unresolved receipt on an older revision blocks the current unpaid bill
  in list, count, detail and payment acceptance. Extra receipts do not regress
  an already paid invoice.

## Deployment and verification

Apply new offline migrations `061_billing_invoices.sql` and
`062_billing_invoice_backfill.sql` only through the controlled migration flow,
after a verified backup. Do not edit historical migration checksums.
Review `deploy/linux/grant-invoice-runtime.sql` against the **existing** runtime
identity and grants; it grants no DDL, deletion, balance or private configuration
access. It must not be used to replace unrelated module permissions.

The settings row disables new checkout/issuance without deleting history.
Verified incoming receipts are retained for review while disabled; refunds and
accepted delivery use existing proofs rather than creating another payment.

Disposable, networking-disabled MySQL suites cover invoice issuance/revisions,
legacy backfill/replay, ownership, actual customer sessions, runtime privileges,
gateway receipt races, balance conservation and product payment boundaries.
`deploy/linux/tests/test_foundation_modules.sh` includes the invoice suites.
The local browser fixture is outside the release repositories; no mock bills or
payment simulation switch is included in the customer application.

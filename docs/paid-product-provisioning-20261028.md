# Paid product provisioning visibility

Paid delivery planning persists one pending product per delivery unit in the same transaction, before supplier I/O. Successful provisioning updates that same identity. Rejected, disconnected, or paid-but-unbound responses preserve the pending product; uncertain purchases must not be replayed.

The instance API exposes deliveryPending and readOnly for unbound pending products. Detail is local-only and server actions are denied until binding. The console shows pending products separately above the default active selection, using the same rows and controls in table and card views. Existing unknown units may restore missing display records through idempotent planning without resetting tasks or making supplier requests; this release does not automatically backfill historical orders.

Procurement failures retain fixed transport phase, completed-write callback, first-byte and duration evidence, without raw URLs, request bodies, passwords or transport errors. These fields do not authorize retries or establish historical supplier acceptance.

Validation: VM Go build/vet, 662 passing unit tests; independently executed MySQL migration, zero-payable and concurrent delivery race tests; synthetic settlement disconnect/rejection/empty-host cases with no extra supplier writes or customer ledger changes. Frontend typecheck/build, 16 focused script tests and real-browser table/card/filter/search/disabled-control checks passed. No real purchase or payment was used for acceptance.

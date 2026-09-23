# Order cancellation and coupon release

Cancellation is a conditional `pending -> cancelled` update scoped to the
authenticated customer. The payment repository uses a conditional `pending ->
paid` update on the same order row. MySQL serializes the competing updates; a
loser cannot overwrite the winner with a stale struct.

The cancellation transaction is passed through the marketing hook. It locks
used coupon instances before clearing their order association, then decrements
their coupon counters in the same transaction. Repeated release is a no-op;
missing/zero counters cause rollback and an explicit reconciliation error.
Historical incorrect counters are not silently reset by this change.

Run on an isolated development host with MySQL installed:

```sh
bash deploy/linux/tests/test_order_transactions.sh
```

The script initializes a separate private `/tmp/ddyidc-foundation-*` data
directory, disables TCP and MySQL X, runs race-enabled tests against its Unix
socket and stops that process. It never reads the application configuration or
business database. Logs/data remain in the printed evidence directory for
inspection. The tests cover ownership, repeated cancellation/release, rollback
on inconsistent coupon statistics and 40 competing payment-state transitions.
The regular test run skips this test without the isolated socket; the dedicated
script is mandatory evidence, not an optional substitute with mocked storage.

This closes the stale-write and split-transaction paths, not the wider external
payment problem: payment can arrive after cancellation at the provider. Late
notifications, reconciliation and refunds remain tracked under S02. No real
gateway callback or purchase is claimed by this database regression.

The workflow under `deploy/ci/` is a template until GitHub workflow write
permission is available. Merely committing it does not activate Actions.

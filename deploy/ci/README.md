# Foundation CI

`foundation-core.yml` is the full preserved foundation suite. Run it manually
with GitHub Actions `workflow_dispatch` when a complete regression is required.

`external-verification.yml` automatically checks the notification, WeChat and
supplier payment safeguards changed during external-configuration acceptance.
It uses isolated tests only: no production credentials, SMS, email, purchases,
renewals, payments or database restores. This split follows the instruction to
run focused checks for this delivery instead of repeating all 351 baseline tests.

Real VM acceptance evidence is maintained separately in the deployment notes;
passing CI does not establish that an external provider delivered a message or
completed a financial transaction.

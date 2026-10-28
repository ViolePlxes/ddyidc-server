# Current customer IDs in presentation

Customer-account presentation fields return decimal strings of the current customer ID. Never convert IDs to JavaScript numbers. Login aliases and password hashes remain unchanged.

Covered: promotion-code owner, public promotion-code verification inviter name, both admin reward parties, sales customer list, and certification review list. Existing recharge, business-order and profile display contracts remain in effect. Search accepts the displayed current ID while retaining existing alias search compatibility.

This is a read-projection change only. No schema/data migration, reassignment, reward settlement, payment, balance mutation or supplier call is introduced. Customer-facing reward privacy remains unchanged. Historical tables retired in v0.1.163 are not restored or read by this change; archive completion metadata must remain intact.

Regression: TestCurrentUserDisplayMySQL runs in deploy/linux/tests/test_home_referral.sh (Foundation CI) with synthetic transaction-rolled-back fixtures, including IDs beyond JavaScript safe integer precision. It verifies owner/beneficiary IDs, exact reward amounts, statuses, stored login aliases/password hashes and sales assignment remain unchanged. Existing private-reward and archived-password tests continue to apply.

# Order supplier admission

Supplier identity belongs to product.upstream_products, keyed by product_id. Neither product.products nor order.orders owns supplier_id. Do not add duplicate columns to work around an invalid query.

PrepareOrder retains supplier/product identity in its opaque preparation result. Normal (including points) and prepaid group commits share transactional mapping lookup and sales admission. No mapping permits self-managed products; changed/ambiguous mappings fail before order, inventory or payment writes. Existing supplier/module sales restrictions remain enforced. Transactions perform no external network calls.

Regression: deploy/linux/tests/test_adapter_modules.sh uses official offline migrations and initializes the actual supplier Store. TestSupplierAdmissionMySQL exercises local protocol stubs, mapped and self-managed orders, replay, mapping changes, ambiguity, disabled suppliers and group rollback/recovery. The existing zero-payable coupon path now runs with Store initialized. Provider protocol/elastic quote suites remain required. A skipped MySQL test is not evidence of success.

User-facing order creation masks MySQL details and returns the server request identifier; logs contain request ID and numeric error code only. No migration is required for this repair.

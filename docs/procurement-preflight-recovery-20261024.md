# Procurement preflight recovery

Production incident: a paid delivery task timed out in Finance POST /cart/get_total before cart_add. The prior v141 cart/settle compatibility fix was present; the new failure occurred earlier.

Local v10 reference CommonCreateTrait::runFinanceCartFlow uses clear/add/settle with full cart_data. Keep the platform current-price preflight and ceiling checks; never bypass cost validation to imitate that sequence.

Only a typed quote transport timeout/network error whose persisted quote checkpoint still has pending invoice and no host can become not_submitted. The service uses a conditional update before returning SafePreflightRetry. Failure to persist or release the delivery-unit claim prevents requeue. Retrying reacquires the same procurement identity and rechecks product auto-setup and loss guard. Customer payment is never repeated.

The fenced delivery-unit task retries at 30/120/300 seconds (four total attempts), then becomes failed with an explicit not-submitted error. Other errors, expired leases and crashes remain unknown. No historical unknown row is reset. The manually replaced incident order must remain untouched.

Tests use isolated MySQL and controlled HTTP supplier: timeout before writes, durable retry, same identity and one final supplier invoice/payment; terminal retry exhaustion; unknown payment never replayed, crash/lease fencing and full Finance cart compatibility regressions. No paid supplier acceptance test.

No migration, credentials, permission or frontend changes. Updating latest main also includes already released v152/v153 features; not the uncommitted paused domain API integration.

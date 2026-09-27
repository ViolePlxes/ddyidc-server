# Foundation candidate acceptance boundaries — 2026-09-27

These changes are a candidate implementation, not a declaration that all eight
foundation packages or production acceptance are complete.

- Local/isolated Go, frontend, finance, ownership and recovery tests have dated
  evidence in the host and VM `deploy-notes/foundation-20260922` directories.
  Skipped tests are not passes; controlled protocol fixtures are not live services.
- Two authorized 0.01 yuan late-payment samples were fully refunded, with user
  receipt confirmation. Alipay notification passed. WeChat was reconciled using
  authenticated official query; live notification remains unverified pending the
  merchant-provided WeChat Pay public verification key and ID. Certificate and
  public-key verification modes have isolated cryptographic regression coverage.
- Live supplier instance 53559 is deleted. It cannot prove normal operation or
  remote-console acceptance. A matching explicit deleted/expired/suspended detail
  may replace stale power state when the power endpoint fails, within the same
  request budget and existing conditional-write protections. Active/unknown or
  mismatched detail must not be treated as successful power verification.
- Deleted instances cannot create new renewal orders or receive control actions.
  Already-paid renewal work remains for manual review rather than issuing another
  external action. Local billing dates are not silently replaced by supplier dates.
- Five authorized isolated restore exercises are exhausted. Recovery remains
  fenced; no additional restore or business release is implied by these commits.
- The latest candidate was upgraded on an empty isolated installation VM. An
  acceptance-harness execute-bit error was corrected before configuration checks
  passed. This is not a fresh production deployment or final tagged-artifact test.
- Live procurement/renewal/SMS, complete role and business matrices, alert delivery,
  real VNC, external recovery reconciliation and GitHub CI activation still require
  their own evidence or prerequisites. No credentials are included in this tree.

The pending external checks do not authorize new payments, supplier purchases,
renewals, messages, destructive recovery, or cloud deployment.

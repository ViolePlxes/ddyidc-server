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
- The isolated restore has completed external-fact reconciliation and explicit
  audited release. Its persistent outbound DROP policy and disabled cron remain;
  this is not production release. Thirty-three restored facts were checked; the
  deleted supplier instance was corrected only in the recovery database. No new
  restore is implied. Root maintenance workers now use separate private runtime
  directories; application directories remain application-owned.
- The latest candidate was upgraded on an empty isolated installation VM. An
  acceptance-harness execute-bit error was corrected before configuration checks
  passed. This is not a fresh production deployment or final tagged-artifact test.
- Live procurement and renewal have explicit limits (one each, 30 yuan each), but
  no charge has occurred: the supplier invoice read returned 403 and the actual
  pre-payment amount cap cannot yet be established. Authorization is not evidence
  of a successful transaction.
- Supplier HTTP and remote-console connections now enforce destination/DNS/port,
  redirect and response-size boundaries. The development VM's proxy DNS supplied
  a reserved fake IP; authenticated DNS-over-TLS resolves the real public endpoint
  without allowing the reserved range. A real read-only product request passed.
- Actual MySQL regression found and corrected the schema-qualified automatic
  renewal alias and asynchronous KILL cleanup timing. Candidate queries reject
  foreign/disabled settings; recovery waits at most five seconds for zero sessions
  and remains closed on failure. Database diagnostics include compiled callsites,
  never query values or driver error text.
- Ordinary sales ownership/field visibility/session revocation passed real HTTP;
  navigation drag and same/new-tab links passed the browser. Product image, cycle
  and quantity updated the real bill. Full cart access remains behind mobile
  binding; no SMS or identity bypass was used. Ticket reads passed; no external
  notification was sent by this read-only browser check.
- Live procurement/renewal/SMS, remaining business matrices, alert delivery, real
  VNC and GitHub CI activation still require their own evidence or prerequisites.
  No credentials are included in this tree.

The pending external checks do not authorize new payments, supplier purchases,
renewals, messages, destructive recovery, or cloud deployment.

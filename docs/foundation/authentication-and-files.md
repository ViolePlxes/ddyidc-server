# F02 authentication and file boundaries

## Account lifecycle

Apply `migrations/20260923_auth_version.sql` with the migration identity before
starting this version. Back up customer/system data first and stop application
writes during the additive upgrade. MySQL 8.0.29+ is required. The runtime checks
for the column and refuses an unmigrated database; it does not install triggers.
The migration is repeatable. Old application binaries ignore the added fields,
but rollback must not be described as preserving the new security protections.

The database increments `auth_version` on password, status, username and customer
soft-delete changes. Ordinary profile/last-login updates do not invalidate a
session. A stale whole-row update cannot reset the version. Password login
passes the version read with the checked credential to issuance. Every new
authorization rechecks enabled state and version against MySQL. Database or
Redis failure denies access. A request authorized before a change is not
retroactively cancelled. No version/cache TTL window is introduced.

Existing cookies lack the version and require signing in after upgrade. The
one-session-per-account behavior remains: a new customer login or impersonation
replaces the previous customer session. Administrator cookies are independent.

## CSRF and delegation

Authenticated unsafe requests require both the readable role-specific CSRF
cookie and `X-CSRF-Token`. The value is HMAC-bound to the session ID and is not
an authentication credential. Authentication cookies remain HttpOnly and
SameSite=Strict; Secure is controlled by the HTTPS deployment configuration.
CSRF must be enabled; an inconsistent disabled setting prevents startup instead
of advertising ineffective protection. Unauthenticated provider callbacks keep
their existing signature checks and do not enter the cookie-session CSRF path.
This does not claim MFA or Passkey support.

Impersonation claims carry administrator actor, customer subject, and parent
administrator session/version. Expiry cannot exceed the parent; parent logout,
replacement, disable or password change invalidates delegation. The frontend
receives only the redirect, never a JWT, and checks the HttpOnly cookie using
`/auth/me`. The synchronous placeholder/new-tab UX is preserved.

Delegated requests persist an actor/subject/request/path audit before invoking
the handler. Audit storage failure rejects execution. Completion records HTTP
status; incomplete records represent unknown completion, not success. No query,
body, credential or session token is stored. Business audits remain unchanged;
request audit supplies the additional actor attribution.

## Ticket uploads

Apply `migrations/20260923_ticket_uploads.sql` before enabling the new binary.
Upload ownership includes both actor role and ID. File type is sniffed from bytes,
not trusted from multipart MIME; storage uses a generated ID and SHA-256. Limits
are 10 MiB/file, 5 files/request, 20 pending files/owner and 50 files/200 MiB per
rolling 24 hours. Failed attempts still count toward the daily anti-abuse quota.

Reservation precedes file writing, so interrupted writes have a registry entry.
Reservations expire after 30 minutes; completed unbound uploads after 24 hours.
The existing leader-controlled maintenance loop collects at most 100 expired
unbound entries per ten minutes. State transitions protect binding against GC;
deletion retries are idempotent. Bound files never enter this collector. Unknown
legacy files are preserved and require a separate inventory, not blind deletion.

Ticket/reply transactions lock and claim only the actor's unexpired unbound
uploads. They replace client-supplied filename/MIME/size with registered values.
Failure rolls back binding as well as ticket/reply writes. Existing historical
ticket links remain readable through their existing ticket authorization; this
change does not infer upload ownership for legacy files or allow rebinding them.
Downloads force attachment, `nosniff` and private/no-store.

## Evidence boundaries

`deploy/linux/tests/test_order_transactions.sh` uses independent real MySQL and
Redis to verify account transitions, the issuance/disable barrier, delegated
parent revocation, atomic attachment ownership/binding and quota/expiry behavior.
It never uses application database credentials. Dedicated tests are required:
their skip in a plain `go test ./...` run is not acceptance evidence.

Real VM browser/HTTP evidence and remaining matrix items are tracked separately
in the dual-written foundation progress directory. Passing these tests alone
does not close the entire production-readiness program.

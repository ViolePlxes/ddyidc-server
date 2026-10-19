# Supplier local read snapshots

The expiring-products list receives its current-cycle renewal amount in its existing local todo response. It does not call a supplier quotation endpoint per row. Payment and actual renewal retain their existing real-time price checks.

Supplier management exposes last full successful synchronization time, last attempt/progress/failure counts, a manual trigger, and per-supplier scheduling (default 120 minutes, 15–1440 minutes, optional disable). Disabling scheduling does not disable manual reads. The existing catalogue synchronization remains separate; this task does not overwrite manual product sale prices.

`GET/PUT/POST /admin/v1/upstream/suppliers/:id/snapshot-sync` require a live super-admin session. PUT takes explicit `enabled` and `intervalMinutes`. Start returns immediately and coalesces an existing run. Read state with GET or refresh the supplier list; no UI polling is introduced.

No migration or extra grant is required: one latest JSON state per supplier in `system.system_configs`, key `upstream.snapshot.<id>`, and one bounded read snapshot in the existing instance `config_snapshot.supplierReadSnapshot`. Credentials, complete provider payloads and duplicate histories are not stored. Supplier-list metadata is loaded in one bulk local query.

Only locally bound active/suspended instances are read. Detail IP/expiry and current-cycle original retail renewal quotes are verified; procurement cost is never reused as retail price. Provenance includes customer, ownership version, supplier, remote resource and cycle. Expiring display rejects mismatched provenance and legacy anonymous price fields. Failed quotes preserve the last valid price with a stale indication; absent verified quotes display pending synchronization. Remote observed lifecycle/power facts do not execute lifecycle actions.

Automatic work runs only under cron leadership. Same-supplier claims use a database row lease, run fence and heartbeat; maximum two supplier workers per process. Manual instance synchronization and automatic reads share a named per-instance lock. Publication rechecks the complete local version and resource ownership. No network call occurs while holding instance row locks. The maintenance admission gate is acquired per bounded item, rather than across the entire batch. Only full success advances `lastSyncedAt`; partial failure/interruption retains prior success and retries within 15 minutes. A process restart can reclaim an expired lease.

Verification: `go build ./...`, `go vet ./...`, `go test ./...`, and `deploy/linux/tests/test_adapter_modules.sh` (fresh disposable MySQL with official migrations, race-enabled supplier coalescing/fencing, HTTP permissions, instance original-price read snapshot, manual-sync regression, expiry local projection). Integration skips in routine tests are registered separately and are never considered passes. VM browser verification covers interval save/restore and three local-only fixture prices. No production purchases or payments are used for acceptance.

Rollback: previous binaries ignore the additional JSON fields; retain business records and all financial history. No shared directory permissions or supplier credentials are changed by this feature.

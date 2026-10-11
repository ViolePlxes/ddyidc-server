# Refund todo read model and deployment checks

Admin refund listing includes verified customer real name and ordered machine IDs/IPs. Queries are read-only. No balance, refund action, legacy identity mapping or schema migration changes are included. Missing IPs remain unavailable; current IPs are not disclosed after ownership transfer.

Acceptance: isolated MySQL association tests, go test ./..., go vet ./..., go build ./..., release artifact check and frontend clipboard/theme browser coverage.

Production release must verify the running /health commit, not only update workspace HEAD. An update helper can refuse installation due to low disk space after the source workspace advances. Preserve the old binary and backups, restart the unchanged service if needed, free only completed staging artifacts, then retry through the online updater. A source checkout matching remote does not prove installation succeeded. Do not upload binaries or perform a manual production Git pull.

Observed 2026-10-11: preflight disk minimum 10240 MiB; build caches may consume available space after the initial check. Staging .new files belonging to old completed tasks are disposable; rollback backups and business data must be retained. Follow-up hardening: keep the old service alive until helper preflight passes and compare installed/running commit before reporting current version.

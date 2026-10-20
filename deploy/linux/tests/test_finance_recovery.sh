#!/usr/bin/env bash
set -euo pipefail
umask 077
root=$(mktemp -d /tmp/ddyidc-foundation-XXXXXXXX)
pid=''
cleanup() {
  if [[ -n "$pid" ]]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
  # Keep initialization/error evidence on failure; no application data touched.
  echo "isolated_mysql_evidence=$root"
}
trap cleanup EXIT
mkdir "$root/data" "$root/files"
/usr/sbin/mysqld --no-defaults --initialize-insecure --datadir="$root/data" --log-error="$root/initialize.log"
/usr/sbin/mysqld --no-defaults --datadir="$root/data" --socket="$root/mysql.sock" --pid-file="$root/mysql.pid" --skip-networking --mysqlx=OFF --secure-file-priv="$root/files" --log-error="$root/mysql.log" &
pid=$!
for n in $(seq 1 60); do
  if mysqladmin --no-defaults --socket="$root/mysql.sock" -uroot ping >/dev/null 2>&1; then break; fi
  sleep 1
done
mysqladmin --no-defaults --socket="$root/mysql.sock" -uroot ping >/dev/null
export FOUNDATION_MYSQL_SOCKET="$root/mysql.sock"
go test -race -v . -run '^TestOfflineMigrationMySQL$' -count=1
go test -race -v ./internal/customer/repository -run '^TestIdentityMutationMySQL$' -count=1
go test -race -v ./internal/payment/repository -run '^TestReceiptRacesMySQL$' -count=1


go test -race -v ./internal/shared/tasks -run '^TestDurable(TaskCrash|Tasks)MySQL$' -count=1

go test -race -v ./internal/billing/balance -run '^TestNotificationBoundariesMySQL$' -count=1

go test -race -v ./internal/order/service -run '^TestZeroPayableMySQL$' -count=1

go test -race -v ./internal/order/service -run '^TestDeliveryUnitsMySQL$' -count=1
go test -race -v ./internal/order/inventory -run '^TestStockReservationsMySQL$' -count=1

go test -race -v ./internal/instance/service -run '^(TestRenewalQueueMySQL|TestRenewalQuoteGateMySQL|TestRenewalQuoteReplacementMySQL|TestAutoRenewalQuotePauseMySQL|TestRenewalQuoteRuntimePrivilegesMySQL)$' -count=1

go test -race -v ./internal/instance/service -run '^TestWebhookIsolationMySQL$' -count=1
go test -race -v ./internal/shared/workgate -run '^TestTwoPoolsDrainBeforePauseMySQL$' -count=1
go test -race -v ./internal/shared/filebarrier -run '^TestSnapshotDefersDeletionMySQL$' -count=1
go test -race -v ./internal/backup -run '^(TestRuntimeAccountFenceMySQL|TestRestoreReleaseAtomicMySQL)$' -count=1

go test -race -v ./internal/payment/service -run "^TestAdminPaymentSettingsMySQL$" -count=1
go test -race -v ./internal/certification -run "^TestPendingManualReviewMySQL$" -count=1

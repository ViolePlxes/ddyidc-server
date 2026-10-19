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
go test -race -v ./internal/upstream/service -run 'TestSupplierSnapshotConcurrencyMySQL|TestAdapterModuleLifecycleMySQL|TestAdapterModuleConcurrentAdmissionMySQL|TestAdapterStaticReleaseRecoveryMySQL|TestComparisonCatalogAndConnectMySQL|TestProductDisconnectHistoryMySQL' -count=1
go test -race -v ./internal/upstream/api -run '^(TestAdapterModuleHTTPPermissionsMySQL|TestSupplierSnapshotHTTPPermissionsMySQL)$' -count=1

go test -race -v ./internal/order/service -run '^(TestCreateRequestIdempotencyMySQL|TestSupplierAdmissionMySQL|TestZeroPayableMySQL)$' -count=1

go test -race -v ./internal/instance/service -run '^TestSupplierReadSnapshotMySQL$' -count=1

ADMIN_INSTANCES_MYSQL_SOCKET="$FOUNDATION_MYSQL_SOCKET" go test -race -v ./internal/instance/service -run '^TestAdminListMySQL$' -count=1

go test -race -v ./internal/admintodo -run '^TestExpiringDisplayMySQL$' -count=1

# Remaining packages run their unit tests; their MySQL suites own separate database lifecycles.
unset FOUNDATION_MYSQL_SOCKET
go test -race -v ./internal/upstream/provider/... ./internal/upstream/api/... ./internal/order/service/... -count=1

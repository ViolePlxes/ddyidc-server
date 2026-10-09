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
go test -race -v ./internal/groupbuy -run '^(TestDurableGroupMySQL|TestIntegrationLifecycle|TestCertificationGateRealDatabase)$' -count=1

go test -race -v ./internal/points -run '^TestPointsIntegration$' -count=1
go test -race -v ./internal/product/repository -run '^TestMoveGroupMySQL$' -count=1
go test -race -v ./internal/marketplace -run '^(TestBalanceEngineLifecycle|TestPushMarketRealDatabase|TestCertificationGateRealDatabase)$' -count=1
go test -race -v ./internal/update -run '^TestRedisGlobalLock$' -count=1

go test -race -v ./internal/videobind -run '^TestRealDatabaseVideoEvents$' -count=1
go test -race -v ./internal/certification -run '^TestAlipayLocalDatabase$' -count=1
go test -race -v ./internal/upstream/service -run '^(TestCatalogReplacementMySQL|TestCatalogMultiNodeMySQL)$' -count=1

go test -race -v ./internal/cdnsell -run '^TestPaidFulfillmentMySQL$' -count=1

go test -race -v ./internal/adminread -run "^TestBalancePermissionsMySQL$" -count=1
go test -race -v ./internal/billing/withdrawal -count=1
go test -race -v ./internal/billing/balance -run "^TestReviewedRefundRequiresApprovalAndFrozenCalculationMySQL$" -count=1

# DDYIDC Linux/amd64 deployment

This directory is the production deployment kit for Ubuntu 22.04 LTS amd64. The application has one supported production platform: Linux/amd64.

## Layout

```text
/opt/ddyidc/bin/               # replaceable executable files (API read-only)
/opt/ddyidc/deploy/            # deployment scripts (API read-only)
/etc/ddyidc/config.yaml        # configuration (mode 0600, API read-only)
/etc/ddyidc/secrets/           # credentials (API read-only)
/var/lib/ddyidc/
  storage/                    # ticket attachments and encrypted certification uploads
  logs/                       # application logs, optional upstream audit log
  tmp/ home/ cache/           # multipart temp files, tool home, Go/npm caches
  run/                        # disk admission marker (PID managed by systemd)
  update/workspaces/          # backend and frontend mutable Git/build workspaces
  update-results/             # plans, staging binaries, backups, archived results
  backups/                    # SQL, Redis, attachments, configuration archives
  migrations/                 # verified original data and migration manifests
/var/lib/mysql/               # separately managed MySQL service data
/var/lib/redis/               # separately managed Redis service data
```

The API mount namespace permits writes only below `/var/lib/ddyidc`. System
journald remains managed by systemd outside the program tree. The dedicated
update helper may replace executables during deployment; the API cannot modify
the program or rebuild that trusted helper. For another data root, change
`Runtime.DataDir`, every Update writable path and the service/script paths
together. Symlink escapes and legacy writable paths outside the root fail startup.

## Existing installation migration

Install `python3-yaml` and `rsync`. Stop the API, update helper and backup timer/
service before migrating. Back up service units and the executable separately.
Run `migrate-runtime.py --config OLD_CONFIG --old-working-directory OLD_CWD` as
root. It copies and SHA-256 verifies runtime files, refuses conflicting files,
preserves existing worktrees, and moves originals into the private migration
archive. Configuration contents are never printed. Install the new units and
scripts, update any local drop-in config paths, then restart and verify health,
uploads, static serving and read-only program mounts. Retain migration archives
until acceptance. Re-running with the new config/data working directory is safe.

For rollback stop the API, restore the backed-up executable/units/config and move
`original-N` directories back according to `migration.json`. Do not merge new
post-migration uploads over old data without reconciling them first. Database
files are not relocated; migrate them with native MySQL/Redis backup and restore.


## Build the delivery package

Run on Linux, WSL2, or a Linux CI runner with Go 1.27.1 and Node 20:

```bash
cd /path/to/ddyidc-server
FRONTEND_ROOT=/path/to/web-react ./deploy/linux/build-linux.sh
```

The output is `deploy/linux/package/` with the two extensionless ELF binaries, the React `dist`, and SHA-256 checksums. Copy the repository deployment directory, including `package`, to the server.

## First installation

Prepare DNS, a fixed SSH administration CIDR, and either a Let's Encrypt email or a certificate pair. The installer is repeatable: it preserves an existing production config and secret files.

```bash
sudo DOMAIN=idc.example.com \
  CERT_MODE=letsencrypt \
  LETSENCRYPT_EMAIL=ops@example.com \
  ./deploy/linux/install.sh

sudo SSH_ALLOW_CIDR=203.0.113.10/32 ./deploy/linux/firewall.sh
```

For an existing certificate:

```bash
sudo DOMAIN=idc.example.com CERT_MODE=files \
  TLS_FULLCHAIN=/secure/fullchain.pem TLS_PRIVKEY=/secure/privkey.pem \
  ./deploy/linux/install.sh
```

The application service is enabled but not started by default, because a new host must first restore the production schemas. After the restore:

```bash
sudo systemctl start ddyidc.service
sudo systemctl enable --now ddyidc-backup.timer ddyidc-disk-monitor.timer
sudo /opt/ddyidc/deploy/verify-linux.sh
```

On a prepared database, set `START_SERVICE=1` when running `install.sh` to start and health-check the API automatically. Service logs are available through `journalctl -u ddyidc.service`; the update helper uses `journalctl -u ddyidc-update-helper.service`.

## Automatic update flow

The backend builds only `linux/amd64`, writes `/var/lib/ddyidc/update-results/pending.plan.json`, and queues the fixed `ddyidc-update-helper.service` through one exact sudoers command. The oneshot helper also runs as the low-privilege `ddyidc` account; sudoers permits only the fixed main-service start/stop/restart commands:

1. checks that the binary volume has at least 10 GiB free and is below the 85% admission threshold;
2. stops `ddyidc.service`;
3. copies a data-directory candidate to a short-lived deployment file, fsyncs and atomically renames it into place; keeps the old binary in `update-results/backups/` (works across filesystems);
4. starts the service and verifies `/health`, the expected commit, and the product smoke endpoint;
5. atomically restores the old binary and Git commit if any check fails.

The helper result is imported into `system.update_tasks` and archived under `update-results/archive/YYYY-MM` for 180 days. The service never performs database migrations automatically.

## MySQL, Redis, and secrets

`install-mysql-redis.sh` installs MySQL 8 and Redis 7, binds both to loopback, creates a non-root `ddyidc_app` account, and generates strong credentials. MySQL uses a 2 GiB buffer pool, 120 connections, one-second slow query logging, and 14-day binlog retention. Redis uses 512 MiB, `noeviction`, RDB plus AOF, protected mode, and an ACL password.

All production database, Redis, JWT, and AES values are regenerated. Source secret files and the rendered application config use mode 0600. Notification and payment credentials must be added only after installation; the Alipay JSON default is `/etc/ddyidc/secrets/alipay.json` and must be owned by `ddyidc` with mode 0600.

Loopback connections do not require database TLS. If MySQL or Redis is later moved to a private database host, enable their native TLS, change the application connection settings, and allow only the application subnet; never publish 3306, 6379, or 8080.

## Backup and restore drill

The 02:00 systemd timer creates a single-transaction all-schema MySQL dump, Redis RDB plus AOF copy, and MySQL binlog bundle. Local retention is 7 daily, 4 weekly, and 12 monthly copies. Set `OBJECT_STORAGE_URI` in the mode-0600 `/etc/ddyidc/backup.env` to upload full backups and incrementally sync binlogs to S3-compatible storage using an instance role or external AWS credentials.

Quarterly restore drill:

```bash
sudo systemctl stop ddyidc.service
sudo mkdir -p /var/tmp/ddyidc-restore
sudo tar -xzf /var/lib/ddyidc/backups/daily/ddyidc-TIMESTAMP.tar.gz -C /var/tmp/ddyidc-restore
sudo mysql --protocol=socket --user=root < /var/tmp/ddyidc-restore/mysql.sql
sudo systemctl stop redis-server
sudo cp /var/tmp/ddyidc-restore/redis.rdb /var/lib/redis/dump.rdb
sudo chown redis:redis /var/lib/redis/dump.rdb
sudo redis-check-rdb /var/lib/redis/dump.rdb
sudo systemctl start redis-server ddyidc.service
curl --fail http://127.0.0.1:8080/health
```

For Redis, stop Redis and restore the tested `redis.rdb` or the complete `redis-aof` directory according to `/etc/redis/redis.conf`; run `redis-check-rdb` or `redis-check-aof` before starting. Perform drills on an isolated host whenever possible, record RPO/RTO, and verify a sample order, payment record, session revocation, and update history.

## Security and operations

- Public firewall rules are limited to 80/443. SSH requires a fixed CIDR, keys only, and no root login.
- Nginx proxies API traffic only to loopback, gives the noVNC WebSocket a one-hour timeout, uses TLS 1.2/1.3, HSTS, CSP, and other Phase 1 security headers.
- Disk monitoring warns at 70%, alerts at 80%, and blocks service/update admission at 85%.
- Nginx and normal application logs retain 30 days; audit, payment, and update records retain 180 days.

## Capacity baseline

Minimum starting specification: 4 dedicated vCPU, 8 GiB RAM, and at least 160 GiB enterprise NVMe with 20% free-space headroom. Allocate roughly 2 GiB to MySQL buffer pool, 512 MiB to Redis, and leave the remainder for the Go API, Nginx, page cache, builds, and update rollback. Recommended for sustained concurrency or local build/update load: 8 vCPU, 16 GiB RAM, and a separate 300+ GiB data/backup volume.

After real traffic is available, run `performance-baseline.sh` for idle and peak samples. Scale when sustained CPU exceeds 65%, available memory drops below 20%, storage latency exceeds 10 ms, or disk usage reaches 70%. The purchasing decision still needs the expected concurrent users, order rate, database size, monthly growth, backup-object-storage vendor, public domain, and fixed SSH CIDR.

# DDYIDC Linux/amd64 deployment

This directory is the production deployment kit for Ubuntu 22.04 LTS amd64. The application has one supported production platform: Linux/amd64.

## Layout

```text
/opt/ddyidc/bin/               # root-owned helpers and fixed server symlink (API/updater read-only)
/opt/ddyidc/application/       # server executable; only update-helper mount permits replacement
/opt/ddyidc/deploy/            # deployment scripts (API read-only)
/etc/ddyidc/config.yaml        # root:ddyidc 0640, API read-only
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
the program or rebuild that trusted helper. The helper remains unprivileged and
cannot replace root-owned backup/bootstrap/helper binaries. Its writable program
mount is limited to `application/`; this is a systemd mount boundary, not two
different Unix application accounts. Do not grant the service account a shell.
For another data root, change
`Runtime.DataDir`, every Update writable path and the service/script paths
together. Symlink escapes and legacy writable paths outside the root fail startup.

## Existing installation migration

`install.sh` is for a fresh installation and refuses an existing config or an
interrupted bootstrap. It runs explicit root-socket migrations before creating
table/column-restricted runtime grants and enabling timers. No schema-wide ALL
grant or existing credential rotation is performed. Use `ddyidc-bootstrap-admin`
to create the first administrator; no default administrator is seeded.

For the program/helper trust boundary, stop the API, updater and backup worker,
then run `migrate-program-layout.sh` as root. It preserves private originals,
verifies identical application bytes, changes the configured update target and
installs a fixed public symlink. Install the updated helper unit before restarting.
It refuses custom paths and repeat/partial attempts for inspection. A failed
partial migration is an offline repair, never an automatic service restart.

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

Run on Linux, WSL2, or a Linux CI runner with Go 1.27.1 and Node 24 LTS (installer default: 24.21.0):

```bash
cd /path/to/ddyidc-server
FRONTEND_ROOT=/path/to/web-react ./deploy/linux/build-linux.sh
```

The output is `deploy/linux/package/` with four ELF binaries (API, update helper, backup worker, administrator bootstrap), the React `dist`, and SHA-256 checksums. Copy the repository deployment directory, including `package`, to the server.

## First installation

Prepare DNS, a fixed SSH administration CIDR, and either a Let's Encrypt email or a certificate pair. This is a fresh-install entry point: existing configuration or interrupted bootstrap causes an explicit refusal, never a credential rotation or automatic retry.

The verified binary/static package can be installed without GitHub credentials. Source-based updates require separately prepared private repository worktrees and credentials. Set `CLONE_UPDATE_WORKTREES=1` only when the service account already has read access to both configured repositories; never pass a token in a repository URL. Until those worktrees are prepared, update preflight must refuse the operation.

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

The installer creates the schema through the explicit offline migration path, then installs restricted runtime credentials. The application service is enabled but not started by default. Create the first administrator with the documented bootstrap command, complete production configuration checks, and then start:

```bash
sudo systemctl start ddyidc.service
sudo systemctl enable --now ddyidc-backup.timer ddyidc-disk-monitor.timer
sudo /opt/ddyidc/deploy/verify-linux.sh
```

For an isolated fresh-install acceptance run, `START_SERVICE=1` also starts and health-checks the API. It does not create a default administrator or restore business data. Existing databases and backup recovery use their separate reviewed procedures. Service logs are available through `journalctl -u ddyidc.service`; the update helper uses `journalctl -u ddyidc-update-helper.service`.

## Automatic update flow

The backend builds only `linux/amd64`, writes `/var/lib/ddyidc/update-results/pending.plan.json`, and queues the fixed `ddyidc-update-helper.service` through one exact sudoers command. The oneshot helper also runs as the low-privilege `ddyidc` account; sudoers permits only the fixed main-service start/stop/restart commands:

1. checks that the binary volume has at least 10 GiB free and is below the 85% admission threshold;
2. stops `ddyidc.service`;
3. copies a data-directory candidate to a short-lived deployment file, fsyncs and atomically renames it into place; keeps the old binary in `update-results/backups/` (works across filesystems);
4. starts the service and verifies `/health`, the expected commit, and the product smoke endpoint;
5. atomically restores the old binary and Git commit if any check fails.

The helper result is imported into `system.update_tasks` and archived under `update-results/archive/YYYY-MM` for 180 days. The service never performs database migrations automatically.

## MySQL, Redis, and secrets

`install-mysql-redis.sh` installs MySQL 8 and the Ubuntu Redis package (supported majors 7/8), binds both to loopback, and generates strong credentials. It does not add a rolling Redis repository or automatically downgrade existing databases. After offline schema migration, `database-identities.py` creates the restricted runtime account. MySQL uses a 2 GiB buffer pool, 120 connections, one-second slow query logging, and 14-day binlog retention. Redis uses 512 MiB, `noeviction`, RDB plus AOF, protected mode, and an ACL password.

All production database, Redis, JWT, and AES values are regenerated. Source/bootstrap secrets use mode 0600; the restricted runtime config is root:ddyidc 0640. The secrets directory permits group traversal only (0710), not directory listing. Notification and payment credentials must be added only after installation; the Alipay JSON default is `/etc/ddyidc/secrets/alipay.json` and must be owned by `ddyidc` with mode 0600.

Loopback connections do not require database TLS. If MySQL or Redis is later moved to a private database host, enable their native TLS, change the application connection settings, and allow only the application subnet; never publish 3306, 6379, or 8080.

## Backup and restore drill

Follow [the F07 recovery contract](../../docs/foundation/backup-recovery-contract.md).
OSS is only the business backup repository: MySQL business schemas and `storage/`,
with format/version/checksum/signature metadata. Never upload configuration,
private keys, the MySQL identity schema, or Redis authentication sessions.

Use the authenticated backup management workflow and the privileged backup worker.
A successful `--verify-oss` verifies the archive; it does not restore a database.
Restore only to a specifically authorized isolated target first, with all writers
paused, runtime database accounts fenced, and external side effects disabled.
Record RPO/RTO and the journal result. A completed import must remain isolated
until external payment/refund/procurement/renewal facts have been reconciled and
an operator explicitly releases it. Do not restart business automatically after
SQL import or restore old Redis sessions.

The former raw `tar`/`mysql`/Redis-copy instructions are intentionally removed:
they bypass compatibility verification, the maintenance barrier, and explicit
business release. Local disaster-recovery scripts must not be treated as a
shortcut around the reviewed worker procedure.

## Security and operations

- Public firewall rules are limited to 80/443. SSH requires a fixed CIDR, keys only, and no root login.
- Nginx proxies API traffic only to loopback, gives the noVNC WebSocket a one-hour timeout, uses TLS 1.2/1.3, HSTS, CSP, and other Phase 1 security headers.
- Disk monitoring warns at 70%, alerts at 80%, and blocks service/update admission at 85%.
- Nginx and normal application log rotation defaults to 30 days. This does not authorize deleting database audit/payment records; balance ledger evidence remains permanent. Review any business-record retention policy separately.

## Capacity baseline

Minimum starting specification: 4 dedicated vCPU, 8 GiB RAM, and at least 160 GiB enterprise NVMe with 20% free-space headroom. Allocate roughly 2 GiB to MySQL buffer pool, 512 MiB to Redis, and leave the remainder for the Go API, Nginx, page cache, builds, and update rollback. Recommended for sustained concurrency or local build/update load: 8 vCPU, 16 GiB RAM, and a separate 300+ GiB data/backup volume.

After real traffic is available, run `performance-baseline.sh` for idle and peak samples. Scale when sustained CPU exceeds 65%, available memory drops below 20%, storage latency exceeds 10 ms, or disk usage reaches 70%. The purchasing decision still needs the expected concurrent users, order rate, database size, monthly growth, backup-object-storage vendor, public domain, and fixed SSH CIDR.

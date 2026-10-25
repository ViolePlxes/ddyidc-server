# Migration source permissions under strict service umask

The controlled migration service uses UMask=0077. Path.mkdir(mode=0755) is still masked to 0700, so the source root and nested archive directories were inaccessible after being mounted into the unprivileged builder. The service failed with status=200/CHDIR before any migration started; old API remained active.

Explicitly set 0755 only on the newly exported credential-free source directories and 0644 on files. Preserve 0700 on WORK, temporary parent and backups, 0600 on credentials. Reject absolute/traversing archive names and symlinks. No recursive permission change to shared configuration trees, no weakening of UMask, mount isolation or network restrictions.

Unit tests export nested source with missing directory archive entries under umask 0077 and verify private parent/file modes remain unchanged. The real cold-build test now also uses umask 0077, the actual migration build identity, protected runtime configuration and blocked non-DNS loopback traffic.

A currently installed root helper cannot repair itself through a failed migration. After CI and tag verification, repair only the root deployment coordinator using the existing transactional installer and independently verified release source. Keep the old business service and all data in place, then retry through the normal online update. Never mark a dirty migration complete, change stored checksums, or install a business binary manually.

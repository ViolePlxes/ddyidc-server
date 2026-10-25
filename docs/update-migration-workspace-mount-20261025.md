# Controlled migration handoff workspace mount

The migration coordinator successfully completed migration 057, runtime verification and unchanged financial checksums, then failed to fast-forward the application checkout: ORIG_HEAD.lock was denied by the service's ProtectSystem=strict mount. Existing ReadWritePaths omitted the fixed backend workspace.

Add only /var/lib/ddyidc/update/workspaces/backend to that service's write allowlist. Preserve strict system protection, private temporary files, umask 0077, NoNewPrivileges and existing account ownership. Git still executes as the runtime UID with hooks disabled and --ff-only; no root Git in the application workspace, no hard reset and no direct business binary installation.

Extend the real systemd regression: create a disposable Git repository, demonstrate that the strict read-only service cannot advance it, then demonstrate that the exact workspace write mount permits a fast-forward under the runtime UID; a neighboring private root file stays unreadable. Cold download, DNS-only firewall and runtime configuration fencing remain covered.

Production migration 057 was independently confirmed complete with no dirty history and running barrier. A subsequent ordinary online update installed v155 without rerunning completed migrations; procurement recovery is already active. This patch prevents future schema updates failing during handoff. Update the deployment coordinator with the existing transactional installer from CI-verified source, then install the new business release through the normal update center. Do not edit checksums, force migrations or use a manual Git reset.

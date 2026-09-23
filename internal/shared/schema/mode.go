// Package schema distinguishes the offline migration process from the runtime.
// Runtime database privileges remain the primary enforcement boundary.
package schema

import "sync/atomic"

var migrationProcess atomic.Bool

func EnableMigrations()       { migrationProcess.Store(true) }
func MigrationsEnabled() bool { return migrationProcess.Load() }

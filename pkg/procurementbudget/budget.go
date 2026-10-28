// Package procurementbudget coordinates bounded supplier procurement work.
package procurementbudget

import "time"

const (
	Request   = 60 * time.Second
	Execution = 5 * time.Minute
	// Leave time for cancellation, durable unknown-state writes and task finish.
	Lease = Execution + time.Minute
)

// Package procurementbudget coordinates bounded supplier procurement work.
package procurementbudget

import "time"

const (
	// Request bounds one supplier write. A settle call provisions the machine
	// before it answers, so 60s was too tight for qkidc and produced http=0
	// timeouts while the remote had not written anything yet.
	Request   = 90 * time.Second
	Execution = 5 * time.Minute
	// Leave time for cancellation, durable unknown-state writes and task finish.
	Lease = Execution + time.Minute
)

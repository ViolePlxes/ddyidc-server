// Package procurementbudget coordinates bounded supplier procurement work.
package procurementbudget

import "time"

const (
	// Request bounds one supplier write. A settle call provisions the machine
	// before it answers and the finance chain logs in, adds to the cart and
	// settles in sequence, so the client must outlast the supplier's own
	// nginx 300s window instead of cutting the connection first.
	Request = 300 * time.Second
	// Execution bounds the whole paid-delivery chain (several 300s requests).
	Execution = 10 * time.Minute
	// Leave time for cancellation, durable unknown-state writes and task finish.
	Lease = Execution + time.Minute
)

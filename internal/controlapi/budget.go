package controlapi

import "time"

// The mutation path crosses several independently timed HTTP layers. Keep
// each outer layer strictly longer than the work it encloses so a completed or
// timed-out mutation can still be authenticated, serialized, and delivered.
const (
	ReadRequestBudget        = 10 * time.Second
	MutationExecutionBudget  = 30 * time.Second
	ControlServerWriteBudget = 35 * time.Second
	WebBridgeMutationBudget  = 40 * time.Second
	WebBridgeWriteBudget     = 45 * time.Second
	MutationClientBudget     = 50 * time.Second
)

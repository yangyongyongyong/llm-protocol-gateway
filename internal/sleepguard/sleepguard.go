// Package sleepguard owns the process-wide system sleep assertion used by the
// gateway. It deliberately prevents only idle system sleep, not display sleep:
// a gateway serving other machines does not need its screen kept awake.
package sleepguard

// Status is safe to render in the console. Enabled is kept by the gateway
// state; Active reports whether the OS assertion is currently held.
type Status struct {
	Available bool   `json:"available"`
	Active    bool   `json:"active"`
	Platform  string `json:"platform"`
	Message   string `json:"message,omitempty"`
}

// Manager is implemented for each supported desktop OS. New never starts an
// assertion; Set(true) creates it and Set(false)/Close release it.
type Manager interface {
	Status() Status
	Set(enabled bool) (Status, error)
	Close() error
}

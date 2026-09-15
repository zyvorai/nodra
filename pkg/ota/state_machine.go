package ota

import "fmt"

var transitions = map[State]map[State]struct{}{
	StatePending: {
		StateDownloading: {}, StateCancelled: {}, StateFailed: {},
	},
	StateDownloading: {
		StateDownloaded: {}, StateCancelled: {}, StateFailed: {},
	},
	StateDownloaded: {
		StateVerifying: {}, StateCancelled: {}, StateFailed: {},
	},
	StateVerifying: {
		StateStaged: {}, StateCancelled: {}, StateFailed: {},
	},
	StateStaged: {
		StateActivating: {}, StateCancelled: {}, StateFailed: {},
	},
	StateActivating: {
		StateRebooting: {}, StateHealthCheck: {}, StateRollbackPending: {}, StateFailed: {},
	},
	StateRebooting: {
		StateHealthCheck: {}, StateRollbackPending: {}, StateFailed: {},
	},
	StateHealthCheck: {
		StateCommitted: {}, StateRollbackPending: {}, StateFailed: {},
	},
	StateRollbackPending: {
		StateRolledBack: {}, StateFailed: {},
	},
}

// CanTransition reports whether next is a valid lifecycle transition.
func CanTransition(current, next State) bool {
	if current == next {
		return true // idempotent status replay after reconnect/restart
	}
	nextStates, ok := transitions[current]
	if !ok {
		return false
	}
	_, ok = nextStates[next]
	return ok
}

// ValidateTransition returns an error for illegal OTA lifecycle transitions.
func ValidateTransition(current, next State) error {
	if !CanTransition(current, next) {
		return fmt.Errorf("ota: invalid state transition %q -> %q", current, next)
	}
	return nil
}

// Terminal reports whether no further successful lifecycle transition is expected.
func (s State) Terminal() bool {
	switch s {
	case StateCommitted, StateFailed, StateRolledBack, StateCancelled:
		return true
	default:
		return false
	}
}

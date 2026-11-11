package services

import "github.com/fhazal/beeircd/internal/state"

// modeChange is one mode letter going on or off, by nick for ranks.
type modeChange struct {
	add  bool
	mode byte
	arg  string
}

func fromState(list []state.ModeChange) []modeChange {
	out := make([]modeChange, len(list))
	for i, m := range list {
		out[i] = modeChange{m.Add, m.Mode, m.Arg}
	}
	return out
}

func umodes(add bool, modes string) []state.ModeChange {
	out := make([]state.ModeChange, len(modes))
	for i := range modes {
		out[i] = state.ModeChange{Add: add, Mode: modes[i]}
	}
	return out
}

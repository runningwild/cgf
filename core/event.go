package core

type Event interface {
	// Applies this event to the Game state.
	Apply(game interface{})
}

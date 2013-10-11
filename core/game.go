package core

type Game interface {
	// Called once each frame after events have been applied for that frame.
	Think()

	// Called once independently on each engine (host and client) before any calls
	// to Think() and before applying any Events.
	InitializeClientData()
}

// clients send events to server, when the server increments the game state
// it sends all clients the pending events.  Each client applies those events
// and thinks.

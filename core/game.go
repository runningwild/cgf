package core

type Game interface {
	// Called once each frame after events have been applied for that frame.
	Think()
}

// clients send events to server, when the server increments the game state
// it sends all clients the pending events.  Each client applies those events
// and thinks.

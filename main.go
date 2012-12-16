package cgf

import (
	"github.com/runningwild/cgf/core"
	"log"
)

type Game interface {
	core.Game
}

type Event interface {
	core.Event
}

type Engine struct {
	server *core.Server
}

func (e *Engine) CurrentState() Game {
	return e.server.CurrentState()
}

func (e *Engine) ApplyEvent(event Event) {
	e.server.ApplyEvent(event)
}

func NewHostEngine(game Game, frame_ms int, logger *log.Logger) (*Engine, error) {
	return nil, nil
}

func NewLocalEngine(game Game, frame_ms int, logger *log.Logger) (*Engine, error) {
	server, err := core.MakeServer(game, frame_ms, logger, nil)
	if err != nil {
		return nil, err
	}
	return &Engine{server}, nil
}

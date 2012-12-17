package cgf

import (
  "fmt"
  "github.com/runningwild/cgf/core"
  "log"
  "net"
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

func (e *Engine) UpdateState(game Game) {
  e.server.UpdateState(game)
}

func (e *Engine) CopyState() Game {
  return e.server.CopyState()
}

func (e *Engine) ApplyEvent(event Event) {
  e.server.ApplyEvent(event)
}

func NewHostEngine(game Game, frame_ms int, ip string, port int, logger *log.Logger) (*Engine, error) {
  listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", ip, port))
  if err != nil {
    return nil, err
  }
  server, err := core.MakeServer(game, frame_ms, logger, listener)
  if err != nil {
    return nil, err
  }
  return &Engine{server}, nil
}

func NewClientEngine(frame_ms int, ip string, port int, logger *log.Logger) (*Engine, error) {
  conn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", ip, port))
  if err != nil {
    return nil, err
  }
  server, err := core.MakeClient(frame_ms, logger, conn)
  if err != nil {
    return nil, err
  }
  return &Engine{server}, nil
}

func NewLocalEngine(game Game, frame_ms int, logger *log.Logger) (*Engine, error) {
  server, err := core.MakeServer(game, frame_ms, logger, nil)
  if err != nil {
    return nil, err
  }
  return &Engine{server}, nil
}

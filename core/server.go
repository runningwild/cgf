package core

import (
	"encoding/gob"
	"errors"
	"log"
	"net"
	"time"
)

type EngineId int64

type EventBundle struct {
	EngineId EngineId
	Events   []Event
}

type CompleteBundle struct {
	// All of the events for the frame, in the order that they should be applied
	Events []Event

	// Optional hash of the game state.  If not nil the client will hash the game
	// state and let the server know if it needs to be resynced.
	Hash []byte
}

type Server struct {
	// Ticks ever ms
	Ticker <-chan time.Time

	game Game

	conns []net.Conn

	remote_bundles chan EventBundle

	// Client stuff
	Events           chan Event
	Complete_bundles chan CompleteBundle
	State_request    chan struct{}
	State_response   chan Game
	Logger           *log.Logger
	Errs             chan error
}

// Exactly one of the values should be non-nil
type wireData struct {
	Err            []byte
	Game           Game
	CompleteBundle *CompleteBundle
}

func MakeServer(game Game, frame_ms int, logger *log.Logger, listener net.Listener) (*Server, error) {
	var s Server
	s.Logger = logger
	s.game = game
	s.Ticker = time.Tick(time.Millisecond * time.Duration(frame_ms))
	s.Events = make(chan Event, 100)
	s.remote_bundles = make(chan EventBundle, 100)
	s.Complete_bundles = make(chan CompleteBundle, 10)
	s.State_request = make(chan struct{})
	s.State_response = make(chan Game)
	s.Errs = make(chan error, 100)
	go s.routine()
	return &s, nil
}

func MakeCilent(data []byte, conn net.Conn) (*Server, error) {
	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)
	err := enc.Encode(data)
	if err != nil {
		return nil, err
	}
	var resp wireData
	err = dec.Decode(&resp)
	if err != nil {
		return nil, err
	}
	if resp.Err != nil {
		return nil, errors.New(string(resp.Err))
	}
	if resp.Game == nil {
		return nil, errors.New("Server failed to send game state.")
	}

	var s Server
	s.game = resp.Game
	s.remote_bundles = make(chan EventBundle, 100)
	s.Events = make(chan Event, 100)
	s.Complete_bundles = make(chan CompleteBundle, 10)
	s.State_request = make(chan struct{})
	s.State_response = make(chan Game)
	s.Errs = make(chan error)
	go s.clientReadRoutine(conn, dec)
	// go s.clientWriteRoutine(conn, enc)
	go s.routine()

	return &s, nil
}

func (s *Server) clientReadRoutine(conn net.Conn, dec *gob.Decoder) {
	for {
		var data wireData
		err := dec.Decode(&data)
		if err != nil {
			s.Errs <- err
			return
		}
		switch {
		case data.Err != nil:
			s.Errs <- errors.New("Server failed to send data.")
			return

		case data.CompleteBundle != nil:
			s.Complete_bundles <- *data.CompleteBundle

		case data.Game != nil:
			// Rawr?~
		}
	}
}

func (s *Server) routine() {
	external_game := s.game.Copy().(Game)
	frame_events := make(map[EngineId][]Event)

	for {
		select {
		// These cases are for all clients
		case event := <-s.Events:
			s.remote_bundles <- EventBundle{0, []Event{event}}

		case bundles := <-s.Complete_bundles:
			for _, event := range bundles.Events {
				event.Apply(s.game)
			}
			s.game.Think()

		case <-s.State_request:
			external_game.OverwriteWith(s.game)
			s.State_response <- external_game

		case err := <-s.Errs:
			// TODO: Better error handling
			panic(err)

		// These cases are for servers only
		case <-s.Ticker:
			var complete_bundle CompleteBundle
			for _, events := range frame_events {
				for _, event := range events {
					complete_bundle.Events = append(complete_bundle.Events, event)
				}
			}
			s.Complete_bundles <- complete_bundle
			frame_events = make(map[EngineId][]Event)
			// TODO: Also send complete bundle to all connected clients

		case bundle := <-s.remote_bundles:
			events := frame_events[bundle.EngineId]
			for _, event := range bundle.Events {
				events = append(events, event)
			}
			frame_events[bundle.EngineId] = events
		}
	}
}

func (s *Server) ApplyEvent(event Event) {
	s.Events <- event
}

func (s *Server) CurrentState() Game {
	s.State_request <- struct{}{}
	return <-s.State_response
}

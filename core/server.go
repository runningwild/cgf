package core

import (
	"encoding/gob"
	"errors"
	"log"
	"net"
	"time"
)

type EngineId int64

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

	// Events received from client engines.
	Remote_events chan Event

	// Completed bundles are sent along here and then sent to each client as well
	// as the server-side client.
	Broadcast_complete_bundles chan CompleteBundle

	// When the server picks up new connections they are sent along this channel.
	New_conns chan net.Conn

	// Current game state.
	game Game

	conns []net.Conn

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
	Event          Event
	CompleteBundle *CompleteBundle
}

func (wd *wireData) GetErr() error {
	if wd.Err != nil {
		return errors.New(string(wd.Err))
	}
	count := 0
	if wd.Game != nil {
		count++
	}
	if wd.Event != nil {
		count++
	}
	if wd.CompleteBundle != nil {
		count++
	}
	if count != 1 {
		return errors.New("wireData was malformed")
	}
	return nil
}

func MakeServer(game Game, frame_ms int, logger *log.Logger, listener net.Listener) (*Server, error) {
	var s Server
	s.Logger = logger
	s.game = game
	s.Ticker = time.Tick(time.Millisecond * time.Duration(frame_ms))
	s.New_conns = make(chan net.Conn, 10)
	s.Broadcast_complete_bundles = make(chan CompleteBundle, 10)
	s.Remote_events = make(chan Event, 100)
	s.Complete_bundles = make(chan CompleteBundle, 10)
	s.State_request = make(chan struct{})
	s.State_response = make(chan Game)
	s.Errs = make(chan error, 100)
	go s.broadcastCompletedBundlesRoutine()
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
	err = resp.GetErr()
	if err != nil {
		return nil, err
	}
	if resp.Game == nil {
		return nil, errors.New("Server failed to send game state.")
	}

	var s Server
	s.game = resp.Game
	s.Events = make(chan Event, 100)
	s.Complete_bundles = make(chan CompleteBundle, 10)
	s.State_request = make(chan struct{})
	s.State_response = make(chan Game)
	s.Errs = make(chan error)
	go s.clientReadRoutine(dec)
	go s.clientWriteRoutine(enc)
	go s.routine()

	return &s, nil
}

// If you're looking for a serverWriteRoutine(), this is basically it.
func (s *Server) broadcastCompletedBundlesRoutine() {
	var clients []*gob.Encoder
	for {
		select {

		// When a bundle is completed we'll send it to each client, and then to
		// ourselves.
		case bundle := <-s.Broadcast_complete_bundles:
			for _, client := range clients {
				err := client.Encode(bundle)
				if err != nil {
					s.Errs <- err
					if s.Logger != nil {
						s.Logger.Printf("Error on encoding, probably should deal with this for realzs.")
					}
				}
			}
			s.Logger.Printf("Sending complete bundle")
			s.Complete_bundles <- bundle
			s.Logger.Printf("Send complete bundle")

		case conn := <-s.New_conns:
			clients = append(clients, gob.NewEncoder(conn))
			go s.serverReadRoutine(gob.NewDecoder(conn))
		}
	}
}

// One of these is launched for each client connected to the server.  It reads
// in events and sends them along the Events channel.
func (s *Server) serverReadRoutine(dec *gob.Decoder) {
	for {
		var data wireData
		err := dec.Decode(&data)
		if err != nil {
			s.Errs <- err
			return
		}
		switch {
		case data.GetErr() != nil:
			s.Errs <- data.GetErr()
			return

		case data.Event != nil:
			s.Remote_events <- data.Event
		}
	}
}

func (s *Server) clientReadRoutine(dec *gob.Decoder) {
	for {
		var data wireData
		err := dec.Decode(&data)
		if err != nil {
			s.Errs <- err
			return
		}
		switch {
		case data.GetErr() != nil:
			s.Errs <- data.GetErr()
			return

		case data.CompleteBundle != nil:
			s.Complete_bundles <- *data.CompleteBundle

		case data.Game != nil:
			// Rawr?
		}
	}
}

func (s *Server) clientWriteRoutine(enc *gob.Encoder) {
	for event := range s.Events {
		var data wireData
		data.Event = event
		err := enc.Encode(data)
		if err != nil {
			s.Errs <- err
			return
		}
	}
}

func (s *Server) routine() {
	external_game := s.game.Copy().(Game)
	complete_bundle := new(CompleteBundle)
	for {
		select {
		// These cases are for all clients
		case bundles := <-s.Complete_bundles:
			s.Logger.Printf("Complete bundle")
			for _, event := range bundles.Events {
				event.Apply(s.game)
			}
			s.game.Think()

		case <-s.State_request:
			s.Logger.Printf("State req")
			external_game.OverwriteWith(s.game)
			s.State_response <- external_game

		case err := <-s.Errs:
			s.Logger.Printf("Err: %v", err)
			// TODO: Better error handling
			panic(err)

		// These cases are for servers only
		case <-s.Ticker:
			s.Logger.Printf("Tick")
			s.Broadcast_complete_bundles <- *complete_bundle
			complete_bundle = new(CompleteBundle)

		case event := <-s.Remote_events:
			s.Logger.Printf("Event")
			complete_bundle.Events = append(complete_bundle.Events, event)
		}
	}
}

func (s *Server) ApplyEvent(event Event) {
	// Exactly one of s.Remote_events and s.Events will be non-nil, depending on
	// if this is a client or a server.
	select {
	case s.Remote_events <- event:
	case s.Events <- event:
	}
}

func (s *Server) CurrentState() Game {
	s.State_request <- struct{}{}
	return <-s.State_response
}

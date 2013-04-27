package core

import (
	"encoding/gob"
	"errors"
	"log"
	"net"
	"sync"
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
	Pause sync.Mutex

	// Ticks ever ms
	Ticker <-chan time.Time

	// Events received from client engines.
	Remote_events chan Event

	// Completed bundles are sent along Buffer_complete_bundle and then sent along
	// Broadcast_complete_bundles to be broadcast to all client machines.  In
	// between they are buffered so that any number of CompleteBundles can be sent
	// without blocking.
	Buffer_complete_bundles    chan CompleteBundle
	Broadcast_complete_bundles chan CompleteBundle

	// When the server picks up new connections they are sent along this channel.
	New_conns chan net.Conn

	// Current game state.
	game Game

	conns []net.Conn

	// Client stuff
	Events           chan Event
	Complete_bundles chan CompleteBundle
	Update_request   chan Game
	Update_response  chan struct{}
	Copy_request     chan struct{}
	Copy_response    chan Game
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

func (s *Server) initCommonChans() {
	s.Complete_bundles = make(chan CompleteBundle, 10)
	s.Update_request = make(chan Game)
	s.Update_response = make(chan struct{})
	s.Copy_request = make(chan struct{})
	s.Copy_response = make(chan Game)
	s.Errs = make(chan error, 100)
}

func (s *Server) initServerChans(frame_ms int) {
	s.Ticker = time.Tick(time.Millisecond * time.Duration(frame_ms))
	s.New_conns = make(chan net.Conn, 10)
	s.Buffer_complete_bundles = make(chan CompleteBundle)
	s.Broadcast_complete_bundles = make(chan CompleteBundle)
	s.Remote_events = make(chan Event, 100)
}

func (s *Server) initClientChans(frame_ms int) {
	s.Events = make(chan Event, 10)
}

func MakeServer(game Game, frame_ms int, logger *log.Logger, listener net.Listener) (*Server, error) {
	var s Server
	s.Logger = logger
	s.game = game
	s.initCommonChans()
	s.initServerChans(frame_ms)
	go s.infiniteBufferRoutine()
	if listener != nil {
		s.New_conns = make(chan net.Conn)
		go s.listenerRoutine(listener)
	}
	go s.broadcastCompletedBundlesRoutine()
	go s.routine()
	return &s, nil
}

func (s *Server) listenerRoutine(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			panic(err)
			s.Errs <- err
			return
		}
		s.New_conns <- conn
	}
}

func MakeClient(frame_ms int, logger *log.Logger, conn net.Conn) (*Server, error) {
	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)
	var resp wireData
	err := dec.Decode(&resp)
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
	s.Logger = logger
	s.game = resp.Game
	s.initCommonChans()
	s.initClientChans(frame_ms)
	go s.clientReadRoutine(dec)
	go s.clientWriteRoutine(enc)
	go s.routine()

	return &s, nil
}

// TODO: should probably kill this off when the engine gets killed off
// Effectively creates an infinitely buffered channel from
// s.Buffer_complete_bundles to s.Broadcast_complete_bundles.
func (s *Server) infiniteBufferRoutine() {
	var bundles []CompleteBundle
	var out chan CompleteBundle
	var dummy_bundle CompleteBundle
	current_bundle := &dummy_bundle
	for {
		select {
		case out <- *current_bundle:
			bundles = bundles[1:]
			if len(bundles) > 0 {
				current_bundle = &bundles[0]
			} else {
				out = nil
			}

		case bundle := <-s.Buffer_complete_bundles:
			bundles = append(bundles, bundle)
			if len(bundles) == 1 {
				out = s.Broadcast_complete_bundles
				current_bundle = &bundles[0]
			}
		}
	}
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
				err := client.Encode(wireData{CompleteBundle: &bundle})
				if err != nil {
					panic(err)
					s.Errs <- err
				}
			}
			// This send is on an unbuffered channel, so if we request the game state
			// after this send completes we will get the most up-to-date state
			// possible.
			s.Complete_bundles <- bundle

		// If we get a new connection we first send them the current game state,
		// then we add them to our list of open connections and launch a routine to
		// handle events they send to us.
		case conn := <-s.New_conns:
			enc := gob.NewEncoder(conn)
			err := enc.Encode(wireData{Game: s.CopyState()})
			if err != nil {
				panic(err)
				s.Errs <- err
			} else {
				clients = append(clients, enc)
				go s.serverReadRoutine(gob.NewDecoder(conn))
			}
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
			panic(err)
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
			panic(err)
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
			panic(err)
			s.Errs <- err
			return
		}
	}
}

func (s *Server) routine() {
	complete_bundle := new(CompleteBundle)
	for {
		select {
		// These cases are for all clients
		case bundles := <-s.Complete_bundles:
			for _, event := range bundles.Events {
				event.Apply(s.game)
			}
			s.Pause.Lock()
			s.game.Think()
			s.Pause.Unlock()

		case game := <-s.Update_request:
			game.OverwriteWith(s.game)
			s.Update_response <- struct{}{}

		case <-s.Copy_request:
			s.Copy_response <- s.game.Copy().(Game)

		case err := <-s.Errs:
			if s.Logger != nil {
				s.Logger.Printf("Errs")
			}
			// TODO: Better error handling
			panic(err)

		// These cases are for servers only
		case <-s.Ticker:
			if s.Logger != nil {
				s.Logger.Printf("Ticker")
			}
			s.Buffer_complete_bundles <- *complete_bundle
			complete_bundle = new(CompleteBundle)

		case event := <-s.Remote_events:
			if s.Logger != nil {
				s.Logger.Printf("Remote_events")
			}
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

func (s *Server) UpdateState(game Game) {
	s.Update_request <- game
	<-s.Update_response
}

func (s *Server) CopyState() Game {
	s.Copy_request <- struct{}{}
	return <-s.Copy_response
}

package core

import (
	"encoding/gob"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"runtime/debug"
	"sync"
	"time"
)

type EngineId int64

type CompleteBundle struct {
	// All of the events for the Frame, in the order that they should be applied
	Events []Event

	// Optional hash of the Game state.  If not nil the client will hash the Game
	// state and let the server know if it needs to be resynced.
	Hash []byte
}

type Server struct {
	id     EngineId
	ids    map[net.Conn]EngineId // Only used on the server
	nextId EngineId
	Pause  sync.RWMutex

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

	// Used to terminate the engine and all associated routines
	KillRoutine        chan struct{}
	KillInfiniteBuffer chan struct{}
	KillBroadcast      chan struct{}

	// Current Game state.
	Game Game

	conns []net.Conn

	// Need to keep this around so that it can be closed if the engine is killed.
	listener net.Listener
	conn     net.Conn

	// Client stuff
	Events           chan Event
	Complete_bundles chan CompleteBundle
	Ids_request      chan struct{}
	Ids_response     chan []int64

	// Run if non-nil and the StackCatcher catches a panic.
	OnCrash func(interface{})

	Logger    *log.Logger
	Errs      chan error
	debugFile *os.File
	Frame     int64
}

type SetupData struct {
	Game  Game
	Id    EngineId
	Frame int64
}

// Exactly one of the values should be non-nil
type wireData struct {
	Err            []byte
	Setup          *SetupData
	Event          Event
	CompleteBundle *CompleteBundle
}

func (wd *wireData) GetErr() error {
	if wd.Err != nil {
		return errors.New(string(wd.Err))
	}
	count := 0
	if wd.Setup != nil {
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
	s.Ids_request = make(chan struct{})
	s.Ids_response = make(chan []int64)
	s.KillRoutine = make(chan struct{})
	s.debugFile, _ = os.Create("/tmp/cgf")
}

func (s *Server) initServerChans(Frame_ms int) {
	s.ids = make(map[net.Conn]EngineId)
	s.id = 1
	s.ids[nil] = 1
	s.nextId = 2
	s.Ticker = time.Tick(time.Millisecond * time.Duration(Frame_ms))
	s.New_conns = make(chan net.Conn, 10)
	s.Buffer_complete_bundles = make(chan CompleteBundle)
	s.Broadcast_complete_bundles = make(chan CompleteBundle)
	s.Remote_events = make(chan Event, 100)
	s.KillInfiniteBuffer = make(chan struct{})
	s.KillBroadcast = make(chan struct{})
}

func (s *Server) initClientChans(Frame_ms int) {
	s.Events = make(chan Event, 10)
}

// If logger is not nil, returns logger, otherwise returns a *log.Logger that
// logs to stderr.
func loggerOrStderr(logger *log.Logger) *log.Logger {
	if logger != nil {
		return logger
	}
	return log.New(os.Stderr, "> ", log.Ltime|log.Lshortfile)
}

func MakeServer(Game Game, Frame_ms int, onCrash func(interface{}), logger *log.Logger, listener net.Listener) (*Server, error) {
	var s Server

	// Lets the cgf main library do some things before any thinks happen.
	s.Pause.RLock()

	s.OnCrash = onCrash
	s.Logger = loggerOrStderr(logger)
	s.Game = Game
	s.Game.InitializeClientData()
	s.initCommonChans()
	s.initServerChans(Frame_ms)
	go s.infiniteBufferRoutine()
	s.listener = listener
	if s.listener != nil {
		s.New_conns = make(chan net.Conn)
		go s.listenerRoutine(s.listener)
	}
	go s.broadcastCompletedBundlesRoutine()
	go s.routine()
	return &s, nil
}

func (s *Server) listenerRoutine(listener net.Listener) {
	defer s.StackCatcher()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if s.Logger != nil {
				s.Logger.Printf("Error while listening: %v", err)
			}
			return
		}
		s.New_conns <- conn
	}
}

func MakeClient(Frame_ms int, onCrash func(interface{}), logger *log.Logger, conn net.Conn) (*Server, error) {
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
	if resp.Setup == nil || resp.Setup.Game == nil {
		return nil, errors.New("Server failed to send initial data.")
	}

	var s Server

	// Lets the cgf main library do some things before any thinks happen.
	s.Pause.RLock()

	s.conn = conn
	s.OnCrash = onCrash
	s.Logger = loggerOrStderr(logger)
	s.Game = resp.Setup.Game
	s.Game.InitializeClientData()
	s.Frame = resp.Setup.Frame
	s.id = resp.Setup.Id
	s.initCommonChans()
	s.initClientChans(Frame_ms)
	go s.clientReadRoutine(dec)
	go s.clientWriteRoutine(enc)
	go s.routine()

	return &s, nil
}

func (s *Server) StackCatcher() {
	if r := recover(); r != nil {
		if s.OnCrash != nil {
			s.OnCrash(r)
		}
		if s.Logger != nil {
			s.Logger.Printf("Panic: %v", r)
			s.Logger.Fatalf("Stack:\n%s", debug.Stack())
		}
	}
}

// Effectively creates an infinitely buffered channel from
// s.Buffer_complete_bundles to s.Broadcast_complete_bundles.
func (s *Server) infiniteBufferRoutine() {
	defer s.StackCatcher()
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

		case <-s.KillInfiniteBuffer:
			return
		}
	}
}

// If you're looking for a serverWriteRoutine(), this is basically it.
func (s *Server) broadcastCompletedBundlesRoutine() {
	defer s.StackCatcher()

	// Keep track of the encoder stream and also the connction that it uses so
	// that we can remove them from s.ids if necessary.
	clients := make(map[*gob.Encoder]net.Conn)

	// Used so that other routines can send us any encoders that failed so we can
	// remove those clients.
	dropped_clients := make(chan *gob.Encoder)

	for {
		select {
		// When a bundle is completed we'll send it to each client, and then to
		// ourselves.
		case bundle := <-s.Broadcast_complete_bundles:
			var dropped []*gob.Encoder
			for client := range clients {
				err := client.Encode(wireData{CompleteBundle: &bundle})
				if err != nil && s.Logger != nil {
					s.Logger.Printf("CGF error on broadcast: %v", err)
					dropped = append(dropped, client)
				}
			}
			for _, enc := range dropped {
				conn := clients[enc]
				delete(clients, enc)
				delete(s.ids, conn)
			}
			// This send is on an unbuffered channel, so if we request the Game state
			// after this send completes we will get the most up-to-date state
			// possible.
			s.Complete_bundles <- bundle

		// If we get a new connection we first send them the current Game state,
		// then we add them to our list of open connections and launch a routine to
		// handle events they send to us.
		case conn := <-s.New_conns:
			enc := gob.NewEncoder(conn)
			if s.Logger != nil {
				s.Logger.Printf("encoding")
			}
			id := s.nextId
			s.nextId++
			s.Pause.RLock()
			err := enc.Encode(wireData{Setup: &SetupData{Game: s.Game, Id: id, Frame: s.Frame}})
			s.Pause.RUnlock()
			if s.Logger != nil {
				s.Logger.Printf("%v", err)
			}
			if err != nil {
				if s.Logger != nil {
					s.Logger.Printf("CGF Error connecting new client: %v", err)
				}
			} else {
				clients[enc] = conn
				s.ids[conn] = id
				go s.serverReadRoutine(
					gob.NewDecoder(conn),
					func() {
						dropped_clients <- enc
					})
			}

		case enc := <-dropped_clients:
			conn := clients[enc]
			delete(clients, enc)
			delete(s.ids, conn)

		case <-s.Ids_request:
			var ids []int64
			for _, id := range s.ids {
				ids = append(ids, int64(id))
			}
			s.Ids_response <- ids

		case <-s.KillBroadcast:
			return
		}
	}
}

// One of these is launched for each client connected to the server.  It reads
// in events and sends them along the Events channel.
// cleanup() is a function that should properly remove all information about the
// remove engine from the local engine.
func (s *Server) serverReadRoutine(dec *gob.Decoder, cleanup func()) {
	defer s.StackCatcher()
	for {
		var data wireData
		err := dec.Decode(&data)
		if err != nil {
			if s.Logger != nil {
				s.Logger.Printf("CGF error reading from client: %v", err)
			}
			cleanup()
			return
		}
		switch {
		case data.GetErr() != nil:
			if s.Logger != nil {
				s.Logger.Printf("CGF error from client: %v", data.GetErr())
			}
			return

		case data.Event != nil:
			s.Remote_events <- data.Event
		}
	}
}

func (s *Server) clientReadRoutine(dec *gob.Decoder) {
	defer s.StackCatcher()
	for {
		var data wireData
		err := dec.Decode(&data)
		if err != nil {
			if s.Logger != nil {
				s.Logger.Printf("CGF client read error: %v", err)
			}
			panic(err)
			return
		}
		switch {
		case data.GetErr() != nil:
			if s.Logger != nil {
				s.Logger.Printf("CGF error from server: %v", data.GetErr())
			}
			return

		case data.CompleteBundle != nil:
			s.Complete_bundles <- *data.CompleteBundle

		case data.Setup != nil:
			// Rawr?
		}
	}
}

func (s *Server) clientWriteRoutine(enc *gob.Encoder) {
	defer s.StackCatcher()
	for event := range s.Events {
		var data wireData
		data.Event = event
		err := enc.Encode(data)
		if err != nil {
			if s.Logger != nil {
				s.Logger.Printf("CGF error writing to client: %v", err)
			}
			return
		}
	}
}

func (s *Server) routine() {
	defer s.StackCatcher()
	complete_bundle := new(CompleteBundle)
	for {
		select {
		// These cases are for all clients
		case bundles := <-s.Complete_bundles:
			s.Pause.Lock()
			fmt.Fprintf(s.debugFile, "Frame %d ----------------\n", s.Frame)
			for _, event := range bundles.Events {
				if s.debugFile != nil {
					fmt.Fprintf(s.debugFile, "%T: %v\n", event, event)
				}
				event.Apply(s.Game)
			}
			s.Game.Think()
			s.Frame++
			s.Pause.Unlock()

		// These cases are for servers only
		case <-s.Ticker:
			s.Buffer_complete_bundles <- *complete_bundle
			complete_bundle = new(CompleteBundle)

		case event := <-s.Remote_events:
			complete_bundle.Events = append(complete_bundle.Events, event)

		case <-s.KillRoutine:
			return
		}
	}
}

func (s *Server) Id() int64 {
	return int64(s.id)
}

func (s *Server) Ids() []int64 {
	if s.id != 1 {
		return nil
	}
	s.Ids_request <- struct{}{}
	return <-s.Ids_response
}

func (s *Server) ApplyEvent(event Event) {
	// Exactly one of s.Remote_events and s.Events will be non-nil, depending on
	// if this is a client or a server.
	select {
	case s.Remote_events <- event:
	case s.Events <- event:
	}
}

func (s *Server) Kill() {
	// close(s.Events)     // This shuts down clientWriteRoutine
	// s.Die <- struct{}{} // One will kill off Server.routine()
	if s.id == 1 {
		// Kill off server routines
		if s.listener != nil {
			// Start off by killing the listener so we can't get any more new
			// connections.
			s.listener.Close()
		}

		// Data goes from the main routine to the infinite buffer to the broadcast
		// routine, so we need to kill them in that order otherwise the main routine
		// might block sending data to the infinite buffer even though it's already
		// died.
		s.KillRoutine <- struct{}{}
		s.KillInfiniteBuffer <- struct{}{}
		s.KillBroadcast <- struct{}{}

		// Also need to close all individual connections.
		for _, conn := range s.conns {
			conn.Close()
		}
	} else {
		// Start by killing the host connection so that nothing new can show up.
		// This will kill off Server.clientReadRoutine()
		s.conn.Close()

		// Kill off Server.clientWriteRoutine()
		close(s.Events)
		s.KillRoutine <- struct{}{} // Server.routine()
	}
}

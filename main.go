package cgf

import (
	"fmt"
	"github.com/runningwild/cgf/core"
	"log"
	"net"
	"time"
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

func (e *Engine) ApplyEvent(event Event) {
	e.server.ApplyEvent(event)
}

func (e *Engine) Pause() {
	e.server.Pause.Lock()
}

func (e *Engine) Unpause() {
	e.server.Pause.Unlock()
}

func (e *Engine) GetState() Game {
	return e.server.Game
}

// Returns the Id of this engine.  Every engine connected in a game has a unique
// id.
func (e *Engine) Id() int64 {
	return e.server.Id()
}

// If this is the Host engine this function will return a list of the ids of all
// engines currently connected, including this engine.  If this is a client
// engine this function will return nil.
func (e *Engine) Ids() []int64 {
	return e.server.Ids()
}

func (e *Engine) Kill() {
	e.server.Kill()
}

func (e *Engine) Host() {
}

type HostData struct {
	Ip   string
	Name string
}

func Host(port int, name string) error {
	fmt.Printf("Hositng...\n")
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	fmt.Printf("Listening...\n")
	listen, err := net.ListenUDP("udp", addr)
	listen.SetReadDeadline(time.Now().Add(time.Second * 10))
	if err != nil {
		return err
	}
	// TODO: Make this not have to run forever
	go func() {
		for {
			msg := make([]byte, 1024)
			n, remoteAddr, err := listen.ReadFromUDP(msg)
			if err != nil {
				return
			}
			var returnPort int
			fmt.Printf("Got ping '%s'...\n", msg[0:n])
			_, err = fmt.Sscanf(string(msg[0:n]), "ping:%d", &returnPort)
			if err != nil {
				continue
			}
			// Take the ping we got and send back a pong so they know we're here
			go func() {
				conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", remoteAddr.IP.String(), returnPort))
				if err != nil {
					return
				}
				fmt.Printf("Sending pong...\n")
				_, err = conn.Write([]byte("pong"))
				if err != nil {
					return
				}
				fmt.Printf("Success!...\n")
				conn.Close()
			}()
		}
	}()
	return nil
}

// This will broadcast on LAN, wait for waitMS milliseconds, and return data for
// each host on the LAN
func SearchLANForHosts(brodacastPort, listenPort int, waitMs int) ([]HostData, error) {
	fmt.Printf("SearchLANForHosts\n")
	// We set up the listener before pinging because we don't want someone to
	// respond before we're listening.
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", listenPort))
	if err != nil {
		return nil, err
	}
	fmt.Printf("Listening...\n")
	listen, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}

	fmt.Printf("Pinging...\n")
	conn, err := net.Dial("udp", fmt.Sprintf("255.255.255.255:%d", brodacastPort))
	if err != nil {
		return nil, err
	}
	_, err = conn.Write([]byte(fmt.Sprintf("ping:%d", listenPort)))
	if err != nil {
		return nil, err
	}

	listen.SetDeadline(time.Now().Add(time.Millisecond * time.Duration(waitMs)))
	var hosts []HostData
	for {
		msg := make([]byte, 1024)
		n, remoteAddr, err := listen.ReadFromUDP(msg)
		if err != nil {
			break
		}
		fmt.Printf("Got response...\n")
		hosts = append(hosts, HostData{Ip: remoteAddr.IP.String(), Name: string(msg[0:n])})
	}

	fmt.Printf("Returning %v...\n", hosts)
	return hosts, nil
}

func NewHostEngine(game Game, frame_ms int, ip string, port int, onCrash func(interface{}), logger *log.Logger) (*Engine, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", ip, port))
	if err != nil {
		return nil, err
	}
	server, err := core.MakeServer(game, frame_ms, onCrash, logger, listener)
	if err != nil {
		return nil, err
	}
	return &Engine{server}, nil
}

func NewClientEngine(frame_ms int, ip string, port int, onCrash func(interface{}), logger *log.Logger) (*Engine, error) {
	conn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", ip, port))
	if err != nil {
		return nil, err
	}
	server, err := core.MakeClient(frame_ms, onCrash, logger, conn)
	if err != nil {
		return nil, err
	}
	return &Engine{server}, nil
}

func NewLocalEngine(game Game, frame_ms int, onCrash func(interface{}), logger *log.Logger) (*Engine, error) {
	server, err := core.MakeServer(game, frame_ms, onCrash, logger, nil)
	if err != nil {
		return nil, err
	}
	return &Engine{server}, nil
}

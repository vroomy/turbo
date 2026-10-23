package turbo

import (
	"fmt"
	"log"
	"net"
	"sync"
)

func New(port uint16, workerCount, queueLength int) (out *Turbo, err error) {
	var l net.Listener
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if l, err = net.Listen("tcp", addr); err != nil {
		return nil, err
	}

	return newTurbo(l, workerCount, queueLength), nil
}

func newTurbo(l net.Listener, workerCount, queueLength int) (out *Turbo) {
	var t Turbo
	t.l = l
	t.conns = make(chan net.Conn, queueLength)

	for range workerCount {
		go t.work()
	}

	return
}

type Turbo struct {
	mux sync.RWMutex

	l net.Listener

	conns   chan net.Conn
	workers sync.WaitGroup

	closed bool
}

func (t *Turbo) work() {
	var err error
	for c := range t.conns {
		if err = t.processConn(c); err != nil {
			log.Printf("error processing connection: %v\n", err)
			if err = c.Close(); err != nil {
				log.Printf("error closing connection: %v\n", err)
			}
		}
	}
}

func (t *Turbo) processConn(c net.Conn) (err error) {

	return nil
}

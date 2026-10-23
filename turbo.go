package turbo

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
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

	t.workers.Add(workerCount)
	for range workerCount {
		go t.work()
	}

	go t.listen()
	return
}

type Turbo struct {
	l net.Listener
	h http.HandlerFunc

	conns   chan net.Conn
	workers sync.WaitGroup

	closed atomic.Bool
}

func (t *Turbo) Close() (err error) {
	if !t.closed.CompareAndSwap(false, true) {
		return nil
	}

	// Wait for workers to finish and close
	t.workers.Wait()
	return nil
}

func (t *Turbo) listen() {
	var err error
	defer close(t.conns)
	for {
		if t.closed.Load() {
			return
		}

		var c net.Conn
		if c, err = t.l.Accept(); err != nil {
			log.Printf("error accepting connection: %v\n", err)
			continue
		}

		t.conns <- c
	}
}

func (t *Turbo) work() {
	var err error
	defer t.workers.Done()
	for c := range t.conns {
		if err = t.process(c); err != nil {
			log.Printf("error processing connection: %v\n", err)
		}
	}
}

func (t *Turbo) process(c net.Conn) (err error) {
	var req *http.Request
	defer closeConn(c)
	r := bufio.NewReader(c)
	if req, err = http.ReadRequest(r); err != nil {
		err = fmt.Errorf("error reading request: %v", err)
		return err
	}

	res := makeResponseWriter()
	res.c = c
	res.head = req.Method == http.MethodHead
	t.h(&res, req)

	if _, err = res.writeHeader(); err != nil {
		return err
	}

	return c.Close()
}

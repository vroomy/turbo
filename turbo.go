package turbo

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
)

var requestReaders = sync.Pool{
	New: func() any { return bufio.NewReader(nil) },
}

func New(port uint16, workerCount, queueLength int) (out *Turbo, err error) {
	return newWithHandler(port, workerCount, queueLength, nil)
}

// NewWithHandler starts a server on 127.0.0.1:port. It installs handler before
// starting workers or accepting connections. Port zero selects an unused port.
func NewWithHandler(port uint16, workerCount, queueLength int, handler http.HandlerFunc) (*Turbo, error) {
	if handler == nil {
		return nil, errors.New("turbo: handler must not be nil")
	}
	if workerCount < 1 || queueLength < 0 {
		return nil, errors.New("turbo: workers must be positive and queue length nonnegative")
	}
	return newWithHandler(port, workerCount, queueLength, handler)
}

func newWithHandler(port uint16, workerCount, queueLength int, handler http.HandlerFunc) (out *Turbo, err error) {
	var l net.Listener
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if l, err = net.Listen("tcp", addr); err != nil {
		return nil, err
	}

	return newTurbo(l, workerCount, queueLength, handler), nil
}

func newTurbo(l net.Listener, workerCount, queueLength int, handler http.HandlerFunc) (out *Turbo) {
	var t Turbo
	t.l = l
	t.h = handler
	t.conns = make(chan net.Conn, queueLength)

	t.workers.Add(workerCount)
	for range workerCount {
		go t.work()
	}

	go t.listen()
	return &t
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

	if t.l != nil {
		err = t.l.Close()
		if errors.Is(err, net.ErrClosed) {
			err = nil
		}
	}

	// Closing the listener releases Accept so listen can close the work queue.
	t.workers.Wait()
	return err
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
			if t.closed.Load() || errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("error accepting connection: %v\n", err)
			continue
		}

		t.conns <- c
	}
}

func (t *Turbo) work() {
	var err error
	defer t.workers.Done()
	res := makeResponseWriter()
	for c := range t.conns {
		if err = t.process(c, &res); err != nil {
			log.Printf("error processing connection: %v\n", err)
		}
	}
}

func (t *Turbo) process(c net.Conn, res *responseWriter) (err error) {
	defer res.reset()
	var req *http.Request
	var body io.ReadCloser
	r := requestReaders.Get().(*bufio.Reader)
	r.Reset(c)
	defer func() {
		if closeErr := finishRequest(c, r, body); err == nil {
			err = closeErr
		}
	}()
	if req, err = http.ReadRequest(r); err != nil {
		err = fmt.Errorf("error reading request: %w", err)
		return err
	}
	body = req.Body

	res.populateHeaders()
	res.c = c
	res.head = req.Method == http.MethodHead
	t.h(res, req)

	if _, err = res.writeHeader(); err != nil {
		return err
	}

	return nil
}

func finishRequest(c net.Conn, reader *bufio.Reader, body io.ReadCloser) error {
	err := closeConn(c)
	// Closing the connection first prevents an unread body from blocking cleanup.
	if body != nil {
		_ = body.Close()
	}
	reader.Reset(nil)
	requestReaders.Put(reader)
	return err
}

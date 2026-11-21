package sonos

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	upnp "github.com/forkcloser/go-upnp"
)

// How long whoever connects to the listener is given: a speaker has
// headerTime to send an event's headers and requestTime to send the whole
// of it, and a connection that says nothing more is kept for idleTime.
// Without them a host on the network holds a connection for as long as
// it likes, by sending slowly or not at all.
const (
	headerTime  = 10 * time.Second
	requestTime = 30 * time.Second
	idleTime    = time.Minute
)

// Events is where speakers send their events: an HTTP server of this
// package's own, listening where its caller said. One serves every speaker
// of every household, and several goroutines may use it.
type Events struct {
	listener   net.Listener
	server     *http.Server
	subscriber *upnp.Subscriber

	// over ends when the server does, with why as its cause.
	over context.Context //nolint:containedctx // the server's life, which every Watch ends with
	end  context.CancelCauseFunc

	closing sync.Once
	served  chan struct{}
}

// Listen opens the listener speakers send their events to, at address: a
// host and a port, "192.0.2.10:49200"; port 0 takes a free one, which Addr
// then gives. The address is the caller's to say, and nothing is guessed:
// the speakers have to reach it, and each Watch is told the URL they reach
// it at.
func Listen(ctx context.Context, address string) (*Events, error) {
	var config net.ListenConfig

	listener, err := config.Listen(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listening for events at %q: %w", address, err)
	}

	subscriber := upnp.NewSubscriber()

	events := &Events{
		listener: listener,
		server: &http.Server{
			Handler:           subscriber,
			ReadHeaderTimeout: headerTime,
			ReadTimeout:       requestTime,
			IdleTimeout:       idleTime,
		},
		subscriber: subscriber,
		served:     make(chan struct{}),
	}

	events.over, events.end = context.WithCancelCause(context.Background())

	go func() {
		defer close(events.served)

		// Serve returns when the server is closed, and otherwise only
		// when the listener can accept no more.
		events.end(fmt.Errorf("%w: the events listener ended: %w", ErrLost, events.server.Serve(listener)))
	}()

	return events, nil
}

// Addr is where the listener listens: the address Listen was given, with
// the port it took. It is not valid for a listener that has no IP address.
func (e *Events) Addr() netip.AddrPort {
	address, ok := e.listener.Addr().(*net.TCPAddr)
	if !ok {
		return netip.AddrPort{}
	}

	return address.AddrPort()
}

// Close ends the listener; every Watch through it returns ErrClosed.
func (e *Events) Close() error {
	var err error

	e.closing.Do(func() {
		e.end(ErrClosed)

		err = e.server.Close()
	})

	<-e.served

	if err != nil {
		return fmt.Errorf("closing the events listener: %w", err)
	}

	return nil
}

// Done is closed when the listener is over: closed, or lost.
func (e *Events) Done() <-chan struct{} {
	return e.over.Done()
}

// Err is nil while the listener lives, ErrClosed after Close, and an ErrLost
// with its cause when it ended by itself.
func (e *Events) Err() error {
	return context.Cause(e.over) //nolint:wrapcheck // the listener's own end, worded where it ended
}

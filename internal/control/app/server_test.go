package app

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServeMarksUnreadyBeforeGracefulShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &controlledServer{
		started:          make(chan struct{}),
		stop:             make(chan struct{}),
		shutdownDeadline: make(chan bool, 1),
	}
	readiness := NewReadiness()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- Serve(ctx, server, listener, readiness, time.Second)
	}()

	<-server.started
	if err := readiness.Check(context.Background()); err != nil {
		t.Fatalf("server was not marked ready: %v", err)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := readiness.Check(context.Background()); err == nil {
		t.Fatal("server remained ready after shutdown started")
	}
	if hasDeadline := <-server.shutdownDeadline; !hasDeadline {
		t.Fatal("shutdown context had no deadline")
	}
	if server.closeCalled {
		t.Fatal("healthy graceful shutdown called Close")
	}
}

type controlledServer struct {
	started          chan struct{}
	stop             chan struct{}
	shutdownDeadline chan bool
	closeCalled      bool
}

func (server *controlledServer) Serve(listener net.Listener) error {
	close(server.started)
	<-server.stop
	_ = listener.Close()
	return http.ErrServerClosed
}

func (server *controlledServer) Shutdown(ctx context.Context) error {
	_, hasDeadline := ctx.Deadline()
	server.shutdownDeadline <- hasDeadline
	close(server.stop)
	return nil
}

func (server *controlledServer) Close() error {
	server.closeCalled = true
	select {
	case <-server.stop:
	default:
		close(server.stop)
	}
	return nil
}

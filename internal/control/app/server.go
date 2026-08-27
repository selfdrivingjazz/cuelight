package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

type LifecycleServer interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
	Close() error
}

func Serve(ctx context.Context, server LifecycleServer, listener net.Listener, readiness *Readiness, shutdownTimeout time.Duration) error {
	if readiness == nil {
		return errors.New("readiness state is required")
	}
	if err := ctx.Err(); err != nil {
		readiness.SetNotReady(err)
		_ = listener.Close()
		return nil
	}

	serveErrors := make(chan error, 1)
	readiness.SetReady()
	go func() {
		serveErrors <- server.Serve(listener)
	}()

	select {
	case err := <-serveErrors:
		readiness.SetNotReady(err)
		return normalizeServeError(err)
	case <-ctx.Done():
		readiness.SetNotReady(errors.New("server is shutting down"))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		shutdownErr = fmt.Errorf("graceful shutdown: %w", shutdownErr)
		if closeErr := server.Close(); closeErr != nil {
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("close server: %w", closeErr))
		}
	}

	select {
	case serveErr := <-serveErrors:
		return errors.Join(shutdownErr, normalizeServeError(serveErr))
	case <-shutdownCtx.Done():
		if closeErr := server.Close(); closeErr != nil {
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("close server: %w", closeErr))
		}
		return errors.Join(shutdownErr, fmt.Errorf("server did not stop before shutdown deadline: %w", shutdownCtx.Err()))
	}
}

func normalizeServeError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve HTTPS: %w", err)
}

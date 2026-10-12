package app

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"github.com/selfdrivingjazz/cuelight/internal/buildinfo"
	"github.com/selfdrivingjazz/cuelight/internal/control/persistence"
	"github.com/selfdrivingjazz/cuelight/migrations"
)

type Runtime struct {
	Config    Config
	Database  *sql.DB
	Readiness *Readiness
}

type HandlerFactory func(Runtime) (http.Handler, error)

type Options struct {
	Handler      HandlerFactory
	ConfigureTLS func(*tls.Config) error
}

func Run(ctx context.Context, config Config, options Options) (resultErr error) {
	slog.Info("cuelight-control starting", "version", buildinfo.Version, "commit", buildinfo.Commit, "listen", config.ListenAddress)
	if err := config.Validate(); err != nil {
		return err
	}
	assets, err := LoadStartupAssets(config)
	if err != nil {
		return fmt.Errorf("validate startup assets: %w", err)
	}

	databaseCtx, cancelDatabase := context.WithTimeout(ctx, config.DatabaseTimeout)
	database, err := persistence.OpenPostgres(databaseCtx, config.DatabaseURL, persistence.PoolConfig{
		MaxOpenConnections: config.DatabaseMaxConnections,
		MaxIdleConnections: config.DatabaseMaxIdle,
		ConnectionLifetime: config.DatabaseConnLifetime,
	})
	cancelDatabase()
	if err != nil {
		return err
	}
	defer func() {
		if err := database.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close PostgreSQL: %w", err))
		}
	}()

	migrationCtx, cancelMigrations := context.WithTimeout(ctx, config.DatabaseTimeout)
	err = persistence.ApplyMigrations(migrationCtx, database, migrations.FS)
	cancelMigrations()
	if err != nil {
		return err
	}

	readiness := NewReadiness(func(probeParent context.Context) error {
		probeCtx, cancel := context.WithTimeout(probeParent, config.DatabaseTimeout)
		defer cancel()
		return database.PingContext(probeCtx)
	})
	runtime := Runtime{Config: config, Database: database, Readiness: readiness}
	var product http.Handler
	if options.Handler != nil {
		product, err = options.Handler(runtime)
		if err != nil {
			return fmt.Errorf("build product handler: %w", err)
		}
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{assets.TLSCertificate},
		MinVersion:   tls.VersionTLS13,
	}
	if options.ConfigureTLS != nil {
		if err := options.ConfigureTLS(tlsConfig); err != nil {
			return fmt.Errorf("configure TLS: %w", err)
		}
		if tlsConfig.MinVersion < tls.VersionTLS13 {
			return errors.New("configure TLS: minimum version must be TLS 1.3 or newer")
		}
		if len(tlsConfig.Certificates) == 0 && tlsConfig.GetCertificate == nil {
			return errors.New("configure TLS: server certificate was removed")
		}
	}

	listener, err := net.Listen("tcp", config.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", config.ListenAddress, err)
	}
	tlsListener := tls.NewListener(listener, tlsConfig)
	server := &http.Server{
		Handler:           NewHandler(buildinfo.Version, readiness, product),
		ReadHeaderTimeout: config.ReadHeaderTimeout,
		IdleTimeout:       config.IdleTimeout,
		MaxHeaderBytes:    config.MaxHeaderBytes,
	}
	return Serve(ctx, server, tlsListener, readiness, config.ShutdownTimeout)
}

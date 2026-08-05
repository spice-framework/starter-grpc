package grpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/spice-framework/spice/lifecycle"
	nativegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// Server owns one native gRPC server and optional standard health service.
type Server struct {
	mu        sync.Mutex
	native    *nativegrpc.Server
	health    *health.Server
	serving   bool
	stopped   bool
	stopOnce  sync.Once
	forceOnce sync.Once
	stopDone  chan struct{}
}

// OpenServer constructs and registers an instance-owned server without
// binding a listener or starting background work.
func OpenServer(
	config ServerConfig,
	registrations []Registration,
	observers ...Observer,
) (*Server, lifecycle.Cleanup, error) {
	limits, err := normalizeLimits(config.Limits)
	if err != nil {
		return nil, nil, fmt.Errorf("construct gRPC server: %w", err)
	}
	tlsSelection, err := normalizeServerTLS(config)
	if err != nil {
		return nil, nil, err
	}
	normalized, err := normalizeRegistrations(registrations)
	if err != nil {
		return nil, nil, err
	}
	if err := validateObservers(observers); err != nil {
		return nil, nil, err
	}
	options := []nativegrpc.ServerOption{
		nativegrpc.MaxRecvMsgSize(limits.MaxReceiveBytes),
		nativegrpc.MaxSendMsgSize(limits.MaxSendBytes),
		nativegrpc.MaxConcurrentStreams(limits.MaxConcurrentStreams),
		nativegrpc.ChainUnaryInterceptor(
			unaryServerInterceptor(observers),
		),
		nativegrpc.ChainStreamInterceptor(
			streamServerInterceptor(observers),
		),
	}
	if tlsSelection.config != nil {
		options = append(
			options,
			nativegrpc.Creds(credentials.NewTLS(tlsSelection.config)),
		)
	}
	native := nativegrpc.NewServer(options...)
	if err := registerServices(native, normalized); err != nil {
		native.Stop()
		return nil, nil, err
	}
	server := &Server{
		native:   native,
		stopDone: make(chan struct{}),
	}
	if config.EnableHealth {
		server.health = health.NewServer()
		healthpb.RegisterHealthServer(native, server.health)
	}
	return server, server.Close, nil
}

// Serve runs the complete registered server on a caller-owned listener. It
// may be called exactly once.
func (server *Server) Serve(listener net.Listener) error {
	if server == nil {
		return errors.New("serve gRPC: server is nil")
	}
	if listener == nil {
		return errors.New("serve gRPC: listener is nil")
	}
	server.mu.Lock()
	if server.stopped {
		server.mu.Unlock()
		return errors.New("serve gRPC: server is stopped")
	}
	if server.serving {
		server.mu.Unlock()
		return errors.New("serve gRPC: server is already serving")
	}
	server.serving = true
	native := server.native
	server.mu.Unlock()
	err := native.Serve(listener)
	if errors.Is(err, nativegrpc.ErrServerStopped) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("serve gRPC: %w", err)
	}
	return nil
}

// SetServing updates the optional standard gRPC health service.
func (server *Server) SetServing(serving bool) error {
	if server == nil {
		return errors.New("set gRPC health: server is nil")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.health == nil {
		return errors.New("set gRPC health: health service is disabled")
	}
	if server.stopped {
		return errors.New("set gRPC health: server is stopped")
	}
	status := healthpb.HealthCheckResponse_NOT_SERVING
	if serving {
		status = healthpb.HealthCheckResponse_SERVING
	}
	server.health.SetServingStatus("", status)
	return nil
}

// Close gracefully drains active RPCs. If the caller's context expires, Close
// force-stops the server and returns the cancellation cause.
func (server *Server) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("close gRPC server: context is nil")
	}
	if server == nil {
		return errors.New("close gRPC server: server is nil")
	}
	server.mu.Lock()
	if server.native == nil {
		server.mu.Unlock()
		return errors.New("close gRPC server: server is invalid")
	}
	server.stopped = true
	native := server.native
	healthServer := server.health
	server.mu.Unlock()
	server.stopOnce.Do(func() {
		if healthServer != nil {
			healthServer.Shutdown()
		}
		go func() {
			native.GracefulStop()
			close(server.stopDone)
		}()
	})
	select {
	case <-server.stopDone:
		return nil
	case <-ctx.Done():
		server.forceOnce.Do(native.Stop)
		<-server.stopDone
		return fmt.Errorf(
			"close gRPC server: %w",
			context.Cause(ctx),
		)
	}
}

func registerServices(
	registrar nativegrpc.ServiceRegistrar,
	registrations []Registration,
) (resultErr error) {
	defer func() {
		if recover() != nil {
			resultErr = errors.New(
				"construct gRPC server: service registration panicked",
			)
		}
	}()
	for _, registration := range registrations {
		if err := registration.Register(registrar); err != nil {
			return fmt.Errorf(
				"construct gRPC server: register service %q: %w",
				registration.Service,
				err,
			)
		}
	}
	return nil
}

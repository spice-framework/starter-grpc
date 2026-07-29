package grpc

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	nativegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestOpenServerAndClientServeObservedRPCs(t *testing.T) {
	t.Parallel()
	observer := &recordingObserver{}
	server, cleanupServer, err := OpenServer(
		ServerConfig{
			AllowInsecure: true,
			EnableHealth:  true,
		},
		[]Registration{pingRegistration(pingServiceFunc(func(
			context.Context,
			*emptypb.Empty,
		) (*emptypb.Empty, error) {
			return &emptypb.Empty{}, nil
		}))},
		observer,
	)
	if err != nil {
		t.Fatal(err)
	}
	if healthErr := server.SetServing(true); healthErr != nil {
		t.Fatal(healthErr)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()
	connection, cleanupClient, err := OpenClient(
		ClientConfig{
			Target:        listener.Addr().String(),
			AllowInsecure: true,
		},
		observer,
	)
	if err != nil {
		t.Fatal(err)
	}
	if invokeErr := connection.Invoke(
		context.Background(),
		"/test.Ping/Ping",
		&emptypb.Empty{},
		&emptypb.Empty{},
	); invokeErr != nil {
		t.Fatal(invokeErr)
	}
	healthResponse, err := healthpb.NewHealthClient(connection).Check(
		context.Background(),
		&healthpb.HealthCheckRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if healthResponse.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health status = %s", healthResponse.Status)
	}
	if err := cleanupClient(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := cleanupClient(context.Background()); err != nil {
		t.Fatalf("second client cleanup = %v", err)
	}
	if err := cleanupServer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-serveResult; err != nil {
		t.Fatal(err)
	}
	results := observer.Results()
	if !containsInteraction(
		results,
		DirectionClient,
		KindUnary,
		"/test.Ping/Ping",
	) || !containsInteraction(
		results,
		DirectionServer,
		KindUnary,
		"/test.Ping/Ping",
	) {
		t.Fatalf("observations = %#v", results)
	}
	for _, result := range results {
		if result.Code != codes.OK || result.Duration < 0 {
			t.Fatalf("observation = %#v", result)
		}
	}
}

func TestServerForceStopsBlockedRPCOnCancellation(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	server, _, err := OpenServer(
		ServerConfig{AllowInsecure: true},
		[]Registration{pingRegistration(pingServiceFunc(func(
			ctx context.Context,
			_ *emptypb.Empty,
		) (*emptypb.Empty, error) {
			close(entered)
			<-ctx.Done()
			return nil, status.Error(codes.Canceled, "stopped")
		}))},
	)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()
	connection, cleanupClient, err := OpenClient(ClientConfig{
		Target:        listener.Addr().String(),
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	rpcResult := make(chan error, 1)
	go func() {
		rpcResult <- connection.Invoke(
			context.Background(),
			"/test.Ping/Ping",
			&emptypb.Empty{},
			&emptypb.Empty{},
		)
	}()
	<-entered
	stopContext, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.Close(
		stopContext,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() error = %v", err)
	}
	if err := <-rpcResult; status.Code(err) != codes.Unavailable {
		t.Fatalf("Invoke() error = %v", err)
	}
	if err := <-serveResult; err != nil {
		t.Fatal(err)
	}
	if err := cleanupClient(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOpenServerRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	certificate := tls.Certificate{Certificate: [][]byte{{1}}}
	validRegistration := pingRegistration(pingServiceFunc(func(
		context.Context,
		*emptypb.Empty,
	) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}))
	tests := []struct {
		name          string
		config        ServerConfig
		registrations []Registration
	}{
		{
			name:          "TLS required",
			config:        ServerConfig{},
			registrations: []Registration{validRegistration},
		},
		{
			name: "TLS and insecure",
			config: ServerConfig{
				TLSConfig: &tls.Config{
					MinVersion:   tls.VersionTLS12,
					Certificates: []tls.Certificate{certificate},
				},
				AllowInsecure: true,
			},
			registrations: []Registration{validRegistration},
		},
		{
			name: "old TLS",
			config: ServerConfig{TLSConfig: &tls.Config{
				MinVersion:   tls.VersionTLS11,
				Certificates: []tls.Certificate{certificate},
			}},
			registrations: []Registration{validRegistration},
		},
		{
			name: "unverified TLS",
			config: ServerConfig{TLSConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: true, //nolint:gosec // Rejection fixture.
				Certificates:       []tls.Certificate{certificate},
			}},
			registrations: []Registration{validRegistration},
		},
		{
			name:          "missing registrations",
			config:        ServerConfig{AllowInsecure: true},
			registrations: nil,
		},
		{
			name:   "duplicate registration",
			config: ServerConfig{AllowInsecure: true},
			registrations: []Registration{
				validRegistration,
				validRegistration,
			},
		},
		{
			name: "invalid limits",
			config: ServerConfig{
				AllowInsecure: true,
				Limits: Limits{
					MaxReceiveBytes: maxMessageBytes + 1,
				},
			},
			registrations: []Registration{validRegistration},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := OpenServer(
				test.config,
				test.registrations,
			); err == nil {
				t.Fatal("OpenServer() error = nil")
			}
		})
	}
}

func TestOpenServerReportsRegistrationFailureAndPanic(t *testing.T) {
	t.Parallel()
	failure := errors.New("registration failed")
	tests := []Registration{
		{
			Service: "test.Failure",
			Register: func(nativegrpc.ServiceRegistrar) error {
				return failure
			},
		},
		{
			Service: "test.Panic",
			Register: func(nativegrpc.ServiceRegistrar) error {
				panic("secret registration value")
			},
		},
	}
	for _, registration := range tests {
		_, _, err := OpenServer(
			ServerConfig{AllowInsecure: true},
			[]Registration{registration},
		)
		if err == nil {
			t.Fatal("OpenServer() error = nil")
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("OpenServer() leaked panic: %v", err)
		}
	}
}

func TestOpenClientRejectsUnsafeTargetsAndTLS(t *testing.T) {
	t.Parallel()
	tests := []ClientConfig{
		{Target: "https://service:443"},
		{Target: "service:0"},
		{
			Target:        "service:443",
			TLSConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			AllowInsecure: true,
		},
		{
			Target: "service:443",
			TLSConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: true, //nolint:gosec // Rejection fixture.
			},
		},
		{
			Target: "service:443",
			TLSConfig: &tls.Config{
				MinVersion: tls.VersionTLS11,
			},
		},
	}
	for _, config := range tests {
		if _, _, err := OpenClient(config); err == nil {
			t.Fatalf("OpenClient(%#v) error = nil", config)
		}
	}
}

func TestConfigurationDefaultsAreBoundedAndDefensive(t *testing.T) {
	t.Parallel()
	limits, err := normalizeLimits(Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if limits.MaxReceiveBytes != defaultMaxMessageBytes ||
		limits.MaxSendBytes != defaultMaxMessageBytes ||
		limits.MaxConcurrentStreams != defaultMaxConcurrentStreams {
		t.Fatalf("limits = %#v", limits)
	}
	tlsSelection, err := normalizeClientTLS(ClientConfig{
		Target: "service.example:443",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tlsSelection.config == nil ||
		tlsSelection.config.ServerName != "service.example" ||
		tlsSelection.config.MinVersion != tls.VersionTLS12 {
		t.Fatalf("client TLS = %#v", tlsSelection.config)
	}
	sourceTLS := &tls.Config{MinVersion: tls.VersionTLS13}
	tlsSelection, err = normalizeClientTLS(ClientConfig{
		Target:    "service.example:443",
		TLSConfig: sourceTLS,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tlsSelection.config == sourceTLS ||
		tlsSelection.config.MinVersion != tls.VersionTLS13 {
		t.Fatal("client TLS was not defensively cloned")
	}
	connection, cleanup, err := OpenClient(ClientConfig{
		Target: "service.example:443",
	})
	if err != nil {
		t.Fatal(err)
	}
	if connection == nil {
		t.Fatal("OpenClient() connection = nil")
	}
	if err := cleanup(nilContext()); err == nil {
		t.Fatal("client cleanup(nil context) error = nil")
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationAndObserverValidation(t *testing.T) {
	t.Parallel()
	if _, err := normalizeRegistrations([]Registration{{
		Service: "not/a/service",
		Register: func(nativegrpc.ServiceRegistrar) error {
			return nil
		},
	}}); err == nil {
		t.Fatal("normalizeRegistrations(invalid name) error = nil")
	}
	if _, err := normalizeRegistrations([]Registration{{
		Service: "test.Missing",
	}}); err == nil {
		t.Fatal("normalizeRegistrations(nil function) error = nil")
	}
	var typedNil *recordingObserver
	if err := validateObservers([]Observer{typedNil}); err == nil {
		t.Fatal("validateObservers(typed nil) error = nil")
	}
}

func TestStreamObserversPropagateContextAndFinishOnce(t *testing.T) {
	t.Parallel()
	observer := &recordingObserver{}
	contextObserver := observerFunc(func(
		ctx context.Context,
		_ Interaction,
	) (context.Context, func(Result)) {
		return context.WithValue(ctx, contextKey{}, "observed"), nil
	})
	serverInterceptor := streamServerInterceptor([]Observer{
		contextObserver,
		observer,
	})
	err := serverInterceptor(
		struct{}{},
		fakeServerStream{
			contextProvider: context.Background,
		},
		&nativegrpc.StreamServerInfo{FullMethod: "/test.Stream/Watch"},
		func(_ any, stream nativegrpc.ServerStream) error {
			if stream.Context().Value(contextKey{}) != "observed" {
				t.Fatal("stream context was not propagated")
			}
			return status.Error(codes.ResourceExhausted, "bounded")
		},
	)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("stream server error = %v", err)
	}
	clientStream := &observedClientStream{
		ClientStream: &fakeClientStream{recvErr: io.EOF},
		interaction: Interaction{
			Direction: DirectionClient,
			Kind:      KindStream,
			Method:    "/test.Stream/Watch",
		},
		started: time.Now(),
		finish: func(result Result) {
			observer.mu.Lock()
			observer.results = append(observer.results, result)
			observer.mu.Unlock()
		},
	}
	if err := clientStream.RecvMsg(&emptypb.Empty{}); !errors.Is(err, io.EOF) {
		t.Fatalf("RecvMsg() error = %v", err)
	}
	if err := clientStream.RecvMsg(&emptypb.Empty{}); !errors.Is(err, io.EOF) {
		t.Fatalf("second RecvMsg() error = %v", err)
	}
	results := observer.Results()
	if len(results) != 2 ||
		results[0].Code != codes.ResourceExhausted ||
		results[1].Code != codes.OK {
		t.Fatalf("stream observations = %#v", results)
	}
}

func TestServerRejectsInvalidUse(t *testing.T) {
	t.Parallel()
	if err := (*Server)(nil).Serve(nil); err == nil {
		t.Fatal("nil server Serve() error = nil")
	}
	if err := (*Server)(nil).SetServing(true); err == nil {
		t.Fatal("nil server SetServing() error = nil")
	}
	if err := (*Server)(nil).Close(context.Background()); err == nil {
		t.Fatal("nil server Close() error = nil")
	}
	server, _, err := OpenServer(
		ServerConfig{AllowInsecure: true},
		[]Registration{pingRegistration(pingServiceFunc(func(
			context.Context,
			*emptypb.Empty,
		) (*emptypb.Empty, error) {
			return &emptypb.Empty{}, nil
		}))},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetServing(true); err == nil {
		t.Fatal("SetServing(disabled) error = nil")
	}
	if err := server.Serve(nil); err == nil {
		t.Fatal("Serve(nil) error = nil")
	}
	if err := server.Close(nilContext()); err == nil {
		t.Fatal("Close(nil context) error = nil")
	}
	if err := server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if err := server.Serve(closedListener{}); err == nil {
		t.Fatal("Serve(after close) error = nil")
	}
}

func TestManifest(t *testing.T) {
	t.Parallel()
	spec := Manifest().Spec()
	if spec.ID != "github.com/StevenBuglione/spice/starter/grpc" ||
		!slices.Equal(spec.Capabilities, []string{
			"rpc.grpc.client",
			"rpc.grpc.server",
		}) ||
		len(spec.Dependencies) != 1 ||
		spec.Dependencies[0].Version != "v1.82.1" {
		t.Fatalf("Manifest() = %#v", spec)
	}
}

type pingService interface {
	Ping(context.Context, *emptypb.Empty) (*emptypb.Empty, error)
}

type pingServiceFunc func(
	context.Context,
	*emptypb.Empty,
) (*emptypb.Empty, error)

func (service pingServiceFunc) Ping(
	ctx context.Context,
	request *emptypb.Empty,
) (*emptypb.Empty, error) {
	return service(ctx, request)
}

func pingRegistration(service pingService) Registration {
	return Registration{
		Service: "test.Ping",
		Register: func(registrar nativegrpc.ServiceRegistrar) error {
			registrar.RegisterService(&pingServiceDescriptor, service)
			return nil
		},
	}
}

var pingServiceDescriptor = nativegrpc.ServiceDesc{
	ServiceName: "test.Ping",
	HandlerType: (*pingService)(nil),
	Methods: []nativegrpc.MethodDesc{{
		MethodName: "Ping",
		Handler: func(
			service any,
			ctx context.Context,
			decode func(any) error,
			interceptor nativegrpc.UnaryServerInterceptor,
		) (any, error) {
			request := &emptypb.Empty{}
			if err := decode(request); err != nil {
				return nil, err
			}
			implementation, ok := service.(pingService)
			if !ok {
				return nil, status.Error(
					codes.Internal,
					"invalid ping service implementation",
				)
			}
			if interceptor == nil {
				return implementation.Ping(ctx, request)
			}
			info := &nativegrpc.UnaryServerInfo{
				Server:     service,
				FullMethod: "/test.Ping/Ping",
			}
			return interceptor(ctx, request, info, func(
				handlerContext context.Context,
				handlerRequest any,
			) (any, error) {
				typedRequest, ok := handlerRequest.(*emptypb.Empty)
				if !ok {
					return nil, status.Error(
						codes.Internal,
						"invalid ping request",
					)
				}
				return implementation.Ping(
					handlerContext,
					typedRequest,
				)
			})
		},
	}},
}

type recordingObserver struct {
	mu      sync.Mutex
	results []Result
}

type observerFunc func(
	context.Context,
	Interaction,
) (context.Context, func(Result))

func (observer observerFunc) BeginRPC(
	ctx context.Context,
	interaction Interaction,
) (context.Context, func(Result)) {
	return observer(ctx, interaction)
}

func (observer *recordingObserver) BeginRPC(
	ctx context.Context,
	_ Interaction,
) (context.Context, func(Result)) {
	return ctx, func(result Result) {
		observer.mu.Lock()
		observer.results = append(observer.results, result)
		observer.mu.Unlock()
	}
}

func (observer *recordingObserver) Results() []Result {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]Result(nil), observer.results...)
}

func containsInteraction(
	results []Result,
	direction Direction,
	kind Kind,
	method string,
) bool {
	return slices.ContainsFunc(results, func(result Result) bool {
		return result.Interaction.Direction == direction &&
			result.Interaction.Kind == kind &&
			result.Interaction.Method == method
	})
}

type closedListener struct{}

func (closedListener) Accept() (net.Conn, error) {
	return nil, net.ErrClosed
}

func (closedListener) Close() error {
	return nil
}

func (closedListener) Addr() net.Addr {
	return &net.TCPAddr{}
}

func nilContext() context.Context {
	return nil
}

type contextKey struct{}

type fakeServerStream struct {
	nativegrpc.ServerStream
	contextProvider func() context.Context
}

func (stream fakeServerStream) Context() context.Context {
	return stream.contextProvider()
}

type fakeClientStream struct {
	nativegrpc.ClientStream
	recvErr error
}

func (stream *fakeClientStream) RecvMsg(any) error {
	return stream.recvErr
}

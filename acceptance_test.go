package grpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	nativegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestMTLSAcceptanceRPCHealthLimitsConcurrencyAndGracefulDrain(t *testing.T) {
	t.Parallel()

	var cancelOnce sync.Once
	var drainOnce sync.Once
	cancelEntered := make(chan struct{})
	drainEntered := make(chan struct{})
	releaseDrain := make(chan struct{})
	service := echoServiceFunc(func(
		ctx context.Context,
		request *wrapperspb.BytesValue,
	) (*wrapperspb.BytesValue, error) {
		switch string(request.Value) {
		case "wait-for-cancel":
			cancelOnce.Do(func() { close(cancelEntered) })
			<-ctx.Done()
			return nil, status.FromContextError(ctx.Err()).Err()
		case "wait-for-drain":
			drainOnce.Do(func() { close(drainEntered) })
			select {
			case <-releaseDrain:
			case <-ctx.Done():
				return nil, status.FromContextError(ctx.Err()).Err()
			}
		}
		return wrapperspb.Bytes(append([]byte(nil), request.Value...)), nil
	})
	observer := &recordingObserver{}
	running := startMTLSServer(t, service, observer, Limits{
		MaxReceiveBytes:      1024,
		MaxSendBytes:         4096,
		MaxConcurrentStreams: 32,
	})

	healthContext, cancelHealth := context.WithTimeout(context.Background(), 2*time.Second)
	healthResponse, err := healthpb.NewHealthClient(running.connection).Check(
		healthContext,
		&healthpb.HealthCheckRequest{},
	)
	cancelHealth()
	if err != nil || healthResponse.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health Check() = %#v, %v", healthResponse, err)
	}

	const concurrentCalls = 16
	callErrors := make(chan error, concurrentCalls)
	var calls sync.WaitGroup
	for index := range concurrentCalls {
		calls.Go(func() {
			payload := fmt.Appendf(nil, "rpc-%02d", index)
			response := &wrapperspb.BytesValue{}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if invokeErr := running.connection.Invoke(
				ctx,
				"/test.Echo/Echo",
				wrapperspb.Bytes(payload),
				response,
			); invokeErr != nil {
				callErrors <- invokeErr
				return
			}
			if string(response.Value) != string(payload) {
				callErrors <- fmt.Errorf("echo response = %q, want %q", response.Value, payload)
			}
		})
	}
	calls.Wait()
	close(callErrors)
	for callErr := range callErrors {
		if callErr != nil {
			t.Fatal(callErr)
		}
	}

	secretPayload := strings.Repeat("private-payload-", 120)
	limitContext, cancelLimit := context.WithTimeout(context.Background(), 2*time.Second)
	err = running.connection.Invoke(
		limitContext,
		"/test.Echo/Echo",
		wrapperspb.Bytes([]byte(secretPayload)),
		&wrapperspb.BytesValue{},
	)
	cancelLimit()
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversized Invoke() error = %v", err)
	}
	if strings.Contains(err.Error(), "private-payload") {
		t.Fatalf("oversized Invoke() leaked payload: %v", err)
	}

	cancelContext, cancelRPC := context.WithCancel(context.Background())
	cancelResult := make(chan error, 1)
	go func() {
		cancelResult <- running.connection.Invoke(
			cancelContext,
			"/test.Echo/Echo",
			wrapperspb.Bytes([]byte("wait-for-cancel")),
			&wrapperspb.BytesValue{},
		)
	}()
	select {
	case <-cancelEntered:
		cancelRPC()
	case <-time.After(2 * time.Second):
		cancelRPC()
		t.Fatal("cancelable RPC did not enter service")
	}
	if err := <-cancelResult; status.Code(err) != codes.Canceled {
		t.Fatalf("canceled Invoke() error = %v", err)
	}

	drainResult := make(chan error, 1)
	go func() {
		drainResult <- running.connection.Invoke(
			context.Background(),
			"/test.Echo/Echo",
			wrapperspb.Bytes([]byte("wait-for-drain")),
			&wrapperspb.BytesValue{},
		)
	}()
	select {
	case <-drainEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("draining RPC did not enter service")
	}
	closeResult := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		closeResult <- running.server.Close(ctx)
	}()
	select {
	case err := <-closeResult:
		t.Fatalf("Close() returned before active RPC drained: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseDrain)
	if err := <-drainResult; err != nil {
		t.Fatalf("draining Invoke() error = %v", err)
	}
	if err := <-closeResult; err != nil {
		t.Fatalf("graceful Close() error = %v", err)
	}

	results := observer.Results()
	if countInteractions(results, DirectionClient, "/test.Echo/Echo") < concurrentCalls+3 ||
		countInteractions(results, DirectionServer, "/test.Echo/Echo") < concurrentCalls+2 {
		t.Fatalf("echo observations = %#v", results)
	}
	if !containsInteractionCode(
		results,
		DirectionClient,
		"/test.Echo/Echo",
		codes.ResourceExhausted,
	) || !containsInteractionCode(
		results,
		DirectionClient,
		"/test.Echo/Echo",
		codes.Canceled,
	) {
		t.Fatalf("failure observations = %#v", results)
	}
	if strings.Contains(fmt.Sprintf("%#v", results), "private-payload") {
		t.Fatalf("observations leaked payload: %#v", results)
	}
}

func TestMTLSAcceptanceRequiresClientCertificateAndForceStops(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	service := echoServiceFunc(func(
		ctx context.Context,
		_ *wrapperspb.BytesValue,
	) (*wrapperspb.BytesValue, error) {
		close(entered)
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	})
	running := startMTLSServer(t, service, &recordingObserver{}, Limits{})

	withoutCertificate := running.clientTLS.Clone()
	withoutCertificate.Certificates = nil
	unauthorized, cleanupUnauthorized, err := OpenClient(ClientConfig{
		Target:    running.target,
		TLSConfig: withoutCertificate,
	})
	if err != nil {
		t.Fatalf("OpenClient(without certificate) error = %v", err)
	}
	unauthorizedContext, cancelUnauthorized := context.WithTimeout(
		context.Background(),
		2*time.Second,
	)
	_, err = healthpb.NewHealthClient(unauthorized).Check(
		unauthorizedContext,
		&healthpb.HealthCheckRequest{},
	)
	cancelUnauthorized()
	if err == nil {
		t.Fatal("health Check(without client certificate) error = nil")
	}
	if strings.Contains(err.Error(), "private-client-identity") {
		t.Fatalf("mTLS failure leaked client identity: %v", err)
	}
	if err := cleanupUnauthorized(context.Background()); err != nil {
		t.Fatalf("cleanup unauthorized client: %v", err)
	}

	rpcResult := make(chan error, 1)
	go func() {
		rpcResult <- running.connection.Invoke(
			context.Background(),
			"/test.Echo/Echo",
			wrapperspb.Bytes([]byte("blocked")),
			&wrapperspb.BytesValue{},
		)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked RPC did not enter service")
	}
	stopContext, cancelStop := context.WithCancel(context.Background())
	cancelStop()
	if err := running.server.Close(stopContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("forced Close() error = %v", err)
	}
	if err := <-rpcResult; status.Code(err) != codes.Unavailable {
		t.Fatalf("forced Invoke() error = %v", err)
	}
}

func TestTLSAcceptanceVerifiesServerCertificateWithoutClientIdentity(t *testing.T) {
	t.Parallel()

	serverTLS, clientTLS := newMTLSConfigs(t)
	serverTLS.ClientAuth = tls.NoClientCert
	serverTLS.ClientCAs = nil
	clientTLS.Certificates = nil
	service := echoServiceFunc(func(
		_ context.Context,
		request *wrapperspb.BytesValue,
	) (*wrapperspb.BytesValue, error) {
		return wrapperspb.Bytes(append([]byte(nil), request.Value...)), nil
	})
	running := startTLSServer(
		t,
		service,
		&recordingObserver{},
		Limits{},
		serverTLS,
		clientTLS,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response := &wrapperspb.BytesValue{}
	if err := running.connection.Invoke(
		ctx,
		"/test.Echo/Echo",
		wrapperspb.Bytes([]byte("verified-tls")),
		response,
	); err != nil {
		t.Fatalf("TLS Invoke() error = %v", err)
	}
	if string(response.Value) != "verified-tls" {
		t.Fatalf("TLS Invoke() response = %q", response.Value)
	}
}

type runningMTLS struct {
	server     *Server
	connection *nativegrpc.ClientConn
	target     string
	clientTLS  *tls.Config
}

func startMTLSServer(
	t *testing.T,
	service echoService,
	observer Observer,
	limits Limits,
) *runningMTLS {
	t.Helper()
	serverTLS, clientTLS := newMTLSConfigs(t)
	return startTLSServer(t, service, observer, limits, serverTLS, clientTLS)
}

func startTLSServer(
	t *testing.T,
	service echoService,
	observer Observer,
	limits Limits,
	serverTLS *tls.Config,
	clientTLS *tls.Config,
) *runningMTLS {
	t.Helper()
	server, cleanupServer, err := OpenServer(
		ServerConfig{
			TLSConfig:    serverTLS,
			Limits:       limits,
			EnableHealth: true,
		},
		[]Registration{echoRegistration(service)},
		observer,
	)
	if err != nil {
		t.Fatalf("OpenServer() error = %v", err)
	}
	if servingErr := server.SetServing(true); servingErr != nil {
		t.Fatalf("SetServing() error = %v", servingErr)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	clientLimits := Limits{
		MaxReceiveBytes:      4096,
		MaxSendBytes:         4096,
		MaxConcurrentStreams: 32,
	}
	connection, cleanupClient, err := OpenClient(
		ClientConfig{
			Target:    listener.Addr().String(),
			TLSConfig: clientTLS,
			Limits:    clientLimits,
		},
		observer,
	)
	if err != nil {
		listenerErr := listener.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cleanupErr := cleanupServer(ctx)
		cancel()
		serveErr := <-serveResult
		t.Fatalf(
			"OpenClient() error = %v; listener close = %v; server cleanup = %v; serve = %v",
			err,
			listenerErr,
			cleanupErr,
			serveErr,
		)
	}
	var cleanupOnce sync.Once
	t.Cleanup(func() {
		cleanupOnce.Do(func() {
			if err := cleanupClient(context.Background()); err != nil {
				t.Errorf("cleanup client: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := cleanupServer(ctx); err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("cleanup server: %v", err)
			}
			select {
			case err := <-serveResult:
				if err != nil {
					t.Errorf("Serve() error = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Error("Serve() did not return after cleanup")
			}
		})
	})
	return &runningMTLS{
		server:     server,
		connection: connection,
		target:     listener.Addr().String(),
		clientTLS:  clientTLS,
	}
}

func newMTLSConfigs(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	caKey := newECDSAKey(t)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Spice test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate(CA) error = %v", err)
	}
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("ParseCertificate(CA) error = %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCertificate)
	serverCertificate := issueCertificate(
		t,
		caCertificate,
		caKey,
		2,
		"localhost",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		[]string{"localhost"},
		[]net.IP{net.ParseIP("127.0.0.1")},
	)
	clientCertificate := issueCertificate(
		t,
		caCertificate,
		caKey,
		3,
		"private-client-identity",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		nil,
		nil,
	)
	return &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{serverCertificate},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    roots,
		}, &tls.Config{
			MinVersion:   tls.VersionTLS12,
			ServerName:   "localhost",
			RootCAs:      roots,
			Certificates: []tls.Certificate{clientCertificate},
		}
}

func issueCertificate(
	t *testing.T,
	ca *x509.Certificate,
	caKey *ecdsa.PrivateKey,
	serial int64,
	commonName string,
	usage []x509.ExtKeyUsage,
	dnsNames []string,
	ipAddresses []net.IP,
) tls.Certificate {
	t.Helper()
	key := newECDSAKey(t)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    ca.NotBefore,
		NotAfter:     ca.NotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usage,
		DNSNames:     dnsNames,
		IPAddresses:  ipAddresses,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate(%s) error = %v", commonName, err)
	}
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
	}
}

func newECDSAKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	return key
}

type echoService interface {
	Echo(context.Context, *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error)
}

type echoServiceFunc func(
	context.Context,
	*wrapperspb.BytesValue,
) (*wrapperspb.BytesValue, error)

func (service echoServiceFunc) Echo(
	ctx context.Context,
	request *wrapperspb.BytesValue,
) (*wrapperspb.BytesValue, error) {
	return service(ctx, request)
}

func echoRegistration(service echoService) Registration {
	return Registration{
		Service: "test.Echo",
		Register: func(registrar nativegrpc.ServiceRegistrar) error {
			registrar.RegisterService(&echoServiceDescriptor, service)
			return nil
		},
	}
}

var echoServiceDescriptor = nativegrpc.ServiceDesc{
	ServiceName: "test.Echo",
	HandlerType: (*echoService)(nil),
	Methods: []nativegrpc.MethodDesc{{
		MethodName: "Echo",
		Handler: func(
			service any,
			ctx context.Context,
			decode func(any) error,
			interceptor nativegrpc.UnaryServerInterceptor,
		) (any, error) {
			request := &wrapperspb.BytesValue{}
			if err := decode(request); err != nil {
				return nil, err
			}
			implementation, ok := service.(echoService)
			if !ok {
				return nil, status.Error(codes.Internal, "invalid echo service implementation")
			}
			if interceptor == nil {
				return implementation.Echo(ctx, request)
			}
			info := &nativegrpc.UnaryServerInfo{
				Server:     service,
				FullMethod: "/test.Echo/Echo",
			}
			return interceptor(ctx, request, info, func(
				handlerContext context.Context,
				handlerRequest any,
			) (any, error) {
				typedRequest, ok := handlerRequest.(*wrapperspb.BytesValue)
				if !ok {
					return nil, status.Error(codes.Internal, "invalid echo request")
				}
				return implementation.Echo(handlerContext, typedRequest)
			})
		},
	}},
}

func countInteractions(results []Result, direction Direction, method string) int {
	count := 0
	for _, result := range results {
		if result.Interaction.Direction == direction && result.Interaction.Method == method {
			count++
		}
	}
	return count
}

func containsInteractionCode(
	results []Result,
	direction Direction,
	method string,
	code codes.Code,
) bool {
	for _, result := range results {
		if result.Interaction.Direction == direction &&
			result.Interaction.Method == method &&
			result.Code == code {
			return true
		}
	}
	return false
}

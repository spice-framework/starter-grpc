package grpc

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/StevenBuglione/spice/lifecycle"
	nativegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// OpenClient constructs an instance-owned client connection without performing
// network I/O. Generated protobuf clients accept the returned connection.
func OpenClient(
	config ClientConfig,
	observers ...Observer,
) (*nativegrpc.ClientConn, lifecycle.Cleanup, error) {
	target, err := normalizeTarget(config.Target)
	if err != nil {
		return nil, nil, err
	}
	config.Target = target
	limits, err := normalizeLimits(config.Limits)
	if err != nil {
		return nil, nil, fmt.Errorf("construct gRPC client: %w", err)
	}
	tlsSelection, err := normalizeClientTLS(config)
	if err != nil {
		return nil, nil, err
	}
	if observerErr := validateObservers(observers); observerErr != nil {
		return nil, nil, observerErr
	}
	var transportCredentials credentials.TransportCredentials
	if tlsSelection.config != nil {
		transportCredentials = credentials.NewTLS(tlsSelection.config)
	} else {
		transportCredentials = insecure.NewCredentials()
	}
	connection, err := nativegrpc.NewClient(
		"dns:///"+target,
		nativegrpc.WithTransportCredentials(transportCredentials),
		nativegrpc.WithDefaultCallOptions(
			nativegrpc.MaxCallRecvMsgSize(limits.MaxReceiveBytes),
			nativegrpc.MaxCallSendMsgSize(limits.MaxSendBytes),
		),
		nativegrpc.WithChainUnaryInterceptor(
			unaryClientInterceptor(observers),
		),
		nativegrpc.WithChainStreamInterceptor(
			streamClientInterceptor(observers),
		),
	)
	if err != nil {
		return nil, nil, errors.New(
			"construct gRPC client: client configuration is invalid",
		)
	}
	var closeOnce sync.Once
	var closeErr error
	cleanup := func(ctx context.Context) error {
		if ctx == nil {
			return errors.New("close gRPC client: context is nil")
		}
		closeOnce.Do(func() {
			if err := connection.Close(); err != nil {
				closeErr = fmt.Errorf("close gRPC client: %w", err)
			}
		})
		return closeErr
	}
	return connection, cleanup, nil
}

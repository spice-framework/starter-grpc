package grpc

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	nativegrpc "google.golang.org/grpc"
)

const (
	defaultMaxMessageBytes      = 4 << 20
	maxMessageBytes             = 16 << 20
	defaultMaxConcurrentStreams = 128
	maxConcurrentStreams        = 4096
	maxRegistrationCount        = 256
	maxIdentityBytes            = 255
)

var serviceNamePattern = regexp.MustCompile(
	`^[A-Za-z][A-Za-z0-9_.]{0,254}$`,
)

// Limits bound RPC memory and server concurrency.
type Limits struct {
	MaxReceiveBytes      int
	MaxSendBytes         int
	MaxConcurrentStreams uint32
}

// ServerConfig defines one explicit gRPC server. A certificate-bearing TLS
// configuration is required unless local development explicitly opts out.
type ServerConfig struct {
	TLSConfig     *tls.Config
	Limits        Limits
	AllowInsecure bool
	EnableHealth  bool
}

// ClientConfig defines one explicit gRPC client connection. Construction does
// not perform network I/O.
type ClientConfig struct {
	Target        string
	TLSConfig     *tls.Config
	Limits        Limits
	AllowInsecure bool
}

// Registration binds one generated protobuf service to a server. Register
// normally calls the generated Register<Service>Server function.
type Registration struct {
	Service  string
	Register func(nativegrpc.ServiceRegistrar) error
}

func normalizeLimits(limits Limits) (Limits, error) {
	if limits.MaxReceiveBytes == 0 {
		limits.MaxReceiveBytes = defaultMaxMessageBytes
	}
	if limits.MaxSendBytes == 0 {
		limits.MaxSendBytes = defaultMaxMessageBytes
	}
	if limits.MaxConcurrentStreams == 0 {
		limits.MaxConcurrentStreams = defaultMaxConcurrentStreams
	}
	if limits.MaxReceiveBytes < 1 ||
		limits.MaxReceiveBytes > maxMessageBytes ||
		limits.MaxSendBytes < 1 ||
		limits.MaxSendBytes > maxMessageBytes {
		return Limits{}, fmt.Errorf(
			"gRPC message limits must be between 1 and %d bytes",
			maxMessageBytes,
		)
	}
	if limits.MaxConcurrentStreams > maxConcurrentStreams {
		return Limits{}, fmt.Errorf(
			"gRPC concurrent stream limit must not exceed %d",
			maxConcurrentStreams,
		)
	}
	return limits, nil
}

type normalizedTLS struct {
	config *tls.Config
}

func normalizeServerTLS(config ServerConfig) (normalizedTLS, error) {
	if config.TLSConfig == nil {
		if config.AllowInsecure {
			return normalizedTLS{}, nil
		}
		return normalizedTLS{}, errors.New(
			"construct gRPC server: TLS certificates are required",
		)
	}
	if config.AllowInsecure {
		return normalizedTLS{}, errors.New(
			"construct gRPC server: TLS and insecure mode are mutually exclusive",
		)
	}
	cloned := config.TLSConfig.Clone()
	if cloned.InsecureSkipVerify {
		return normalizedTLS{}, errors.New(
			"construct gRPC server: insecure TLS verification settings are not allowed",
		)
	}
	if len(cloned.Certificates) == 0 {
		return normalizedTLS{}, errors.New(
			"construct gRPC server: TLS certificates are required",
		)
	}
	if cloned.MinVersion == 0 {
		cloned.MinVersion = tls.VersionTLS12
	}
	if cloned.MinVersion < tls.VersionTLS12 {
		return normalizedTLS{}, errors.New(
			"construct gRPC server: TLS 1.2 or newer is required",
		)
	}
	return normalizedTLS{config: cloned}, nil
}

func normalizeClientTLS(config ClientConfig) (normalizedTLS, error) {
	if config.AllowInsecure {
		if config.TLSConfig != nil {
			return normalizedTLS{}, errors.New(
				"construct gRPC client: TLS and insecure mode are mutually exclusive",
			)
		}
		return normalizedTLS{}, nil
	}
	var cloned *tls.Config
	if config.TLSConfig == nil {
		host, _, err := net.SplitHostPort(config.Target)
		if err != nil {
			return normalizedTLS{}, errors.New(
				"construct gRPC client: target must be an exact host:port",
			)
		}
		cloned = &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: host,
		}
	} else {
		cloned = config.TLSConfig.Clone()
	}
	if cloned.InsecureSkipVerify {
		return normalizedTLS{}, errors.New(
			"construct gRPC client: TLS certificate verification is required",
		)
	}
	if cloned.MinVersion == 0 {
		cloned.MinVersion = tls.VersionTLS12
	}
	if cloned.MinVersion < tls.VersionTLS12 {
		return normalizedTLS{}, errors.New(
			"construct gRPC client: TLS 1.2 or newer is required",
		)
	}
	return normalizedTLS{config: cloned}, nil
}

func normalizeTarget(target string) (string, error) {
	if target == "" ||
		len(target) > maxIdentityBytes ||
		strings.TrimSpace(target) != target ||
		strings.ContainsAny(target, "\x00\r\n\t /") {
		return "", errors.New(
			"construct gRPC client: target must be an exact host:port",
		)
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return "", errors.New(
			"construct gRPC client: target must be an exact host:port",
		)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return "", errors.New(
			"construct gRPC client: target port must be between 1 and 65535",
		)
	}
	return target, nil
}

func normalizeRegistrations(
	registrations []Registration,
) ([]Registration, error) {
	if len(registrations) == 0 ||
		len(registrations) > maxRegistrationCount {
		return nil, fmt.Errorf(
			"construct gRPC server: registrations must contain 1 to %d services",
			maxRegistrationCount,
		)
	}
	normalized := append([]Registration(nil), registrations...)
	slices.SortFunc(normalized, func(left, right Registration) int {
		return strings.Compare(left.Service, right.Service)
	})
	for index, registration := range normalized {
		if !serviceNamePattern.MatchString(registration.Service) ||
			registration.Register == nil {
			return nil, errors.New(
				"construct gRPC server: every registration requires a safe service name and function",
			)
		}
		if index > 0 &&
			registration.Service == normalized[index-1].Service {
			return nil, fmt.Errorf(
				"construct gRPC server: duplicate service %q",
				registration.Service,
			)
		}
	}
	return normalized, nil
}

func validateObservers(observers []Observer) error {
	for index, observer := range observers {
		if nilInterface(observer) {
			return fmt.Errorf(
				"construct gRPC integration: observer %d is nil",
				index,
			)
		}
	}
	return nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() { //nolint:exhaustive // Only nil-capable reflection kinds can be nil.
	case reflect.Chan, reflect.Func, reflect.Interface,
		reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

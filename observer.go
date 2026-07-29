// Package grpc provides reviewed, instance-owned gRPC server and client
// integration for Spice applications.
package grpc

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"time"

	nativegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Direction identifies which side of an RPC emitted an observation.
type Direction string

const (
	// DirectionClient identifies an outbound client RPC.
	DirectionClient Direction = "client"
	// DirectionServer identifies an inbound server RPC.
	DirectionServer Direction = "server"
)

// Kind identifies the RPC transport shape.
type Kind string

const (
	// KindUnary identifies one request and one response.
	KindUnary Kind = "unary"
	// KindStream identifies a client, server, or bidirectional stream.
	KindStream Kind = "stream"
)

// Interaction contains payload-free RPC facts.
type Interaction struct {
	Direction Direction
	Kind      Kind
	Method    string
}

// Result describes one completed RPC without exposing request or response
// values.
type Result struct {
	Interaction Interaction
	Code        codes.Code
	Duration    time.Duration
}

// Observer receives RPC begin/end information. Implementations must not add
// request or response payloads to logs, metrics, or traces.
type Observer interface {
	BeginRPC(context.Context, Interaction) (context.Context, func(Result))
}

func unaryServerInterceptor(
	observers []Observer,
) nativegrpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		request any,
		info *nativegrpc.UnaryServerInfo,
		handler nativegrpc.UnaryHandler,
	) (any, error) {
		interaction := Interaction{
			Direction: DirectionServer,
			Kind:      KindUnary,
			Method:    info.FullMethod,
		}
		observedContext, finish := beginObservers(ctx, interaction, observers)
		started := time.Now()
		response, err := handler(observedContext, request)
		finish(resultFor(interaction, started, err))
		return response, err
	}
}

func streamServerInterceptor(
	observers []Observer,
) nativegrpc.StreamServerInterceptor {
	return func(
		service any,
		stream nativegrpc.ServerStream,
		info *nativegrpc.StreamServerInfo,
		handler nativegrpc.StreamHandler,
	) error {
		interaction := Interaction{
			Direction: DirectionServer,
			Kind:      KindStream,
			Method:    info.FullMethod,
		}
		observedContext, finish := beginObservers(
			stream.Context(),
			interaction,
			observers,
		)
		started := time.Now()
		err := handler(service, observedServerStream{
			ServerStream: stream,
			contextProvider: func() context.Context {
				return observedContext
			},
		})
		finish(resultFor(interaction, started, err))
		return err
	}
}

func unaryClientInterceptor(
	observers []Observer,
) nativegrpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		request any,
		reply any,
		connection *nativegrpc.ClientConn,
		invoker nativegrpc.UnaryInvoker,
		options ...nativegrpc.CallOption,
	) error {
		interaction := Interaction{
			Direction: DirectionClient,
			Kind:      KindUnary,
			Method:    method,
		}
		observedContext, finish := beginObservers(ctx, interaction, observers)
		started := time.Now()
		err := invoker(
			observedContext,
			method,
			request,
			reply,
			connection,
			options...,
		)
		finish(resultFor(interaction, started, err))
		return err
	}
}

func streamClientInterceptor(
	observers []Observer,
) nativegrpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		description *nativegrpc.StreamDesc,
		connection *nativegrpc.ClientConn,
		method string,
		streamer nativegrpc.Streamer,
		options ...nativegrpc.CallOption,
	) (nativegrpc.ClientStream, error) {
		interaction := Interaction{
			Direction: DirectionClient,
			Kind:      KindStream,
			Method:    method,
		}
		observedContext, finish := beginObservers(ctx, interaction, observers)
		started := time.Now()
		stream, err := streamer(
			observedContext,
			description,
			connection,
			method,
			options...,
		)
		if err != nil {
			finish(resultFor(interaction, started, err))
			return nil, err
		}
		return &observedClientStream{
			ClientStream: stream,
			interaction:  interaction,
			started:      started,
			finish:       finish,
		}, nil
	}
}

type observedServerStream struct {
	nativegrpc.ServerStream
	contextProvider func() context.Context
}

func (stream observedServerStream) Context() context.Context {
	return stream.contextProvider()
}

type observedClientStream struct {
	nativegrpc.ClientStream
	interaction Interaction
	started     time.Time
	finish      func(Result)
	finishOnce  sync.Once
}

func (stream *observedClientStream) RecvMsg(message any) error {
	err := stream.ClientStream.RecvMsg(message)
	if err != nil {
		stream.finishResult(err)
	}
	return err
}

func (stream *observedClientStream) finishResult(err error) {
	if errors.Is(err, io.EOF) {
		err = nil
	}
	stream.finishOnce.Do(func() {
		stream.finish(resultFor(stream.interaction, stream.started, err))
	})
}

func resultFor(
	interaction Interaction,
	started time.Time,
	err error,
) Result {
	return Result{
		Interaction: interaction,
		Code:        status.Code(err),
		Duration:    time.Since(started),
	}
}

func beginObservers(
	ctx context.Context,
	interaction Interaction,
	observers []Observer,
) (context.Context, func(Result)) {
	finishers := make([]func(Result), 0, len(observers))
	observedContext := beginObserverChain(
		ctx,
		interaction,
		observers,
		&finishers,
	)
	return observedContext, func(result Result) {
		for _, finish := range slices.Backward(finishers) {
			finish(result)
		}
	}
}

func beginObserverChain(
	ctx context.Context,
	interaction Interaction,
	observers []Observer,
	finishers *[]func(Result),
) context.Context {
	if len(observers) == 0 {
		return ctx
	}
	next, finish := observers[0].BeginRPC(ctx, interaction)
	if next == nil {
		next = ctx
	}
	if finish != nil {
		*finishers = append(*finishers, finish)
	}
	return beginObserverChain(
		next,
		interaction,
		observers[1:],
		finishers,
	)
}

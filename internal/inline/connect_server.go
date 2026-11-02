package inline

import (
	"context"
	stderrors "errors"
	"fmt"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/networkservicemesh/api/pkg/api/networkservice"
	"github.com/networkservicemesh/sdk/pkg/networkservice/core/next"
	"github.com/networkservicemesh/sdk/pkg/tools/log"
	"github.com/networkservicemesh/sdk/pkg/tools/postpone"
)

type connectServer struct {
	client      networkservice.NetworkServiceClient
	callOptions []grpc.CallOption
}

func NewConnectServer(client networkservice.NetworkServiceClient, callOptions ...grpc.CallOption) networkservice.NetworkServiceServer {
	return &connectServer{
		client:      client,
		callOptions: callOptions,
	}
}

func (c *connectServer) Request(ctx context.Context, request *networkservice.NetworkServiceRequest) (*networkservice.Connection, error) {
	if request.GetConnection() == nil {
		return nil, fmt.Errorf("request connection is required")
	}

	logrus.Infof("inline connect request entered: %s", requestSummary(request))
	logger := log.FromContext(ctx)
	if downstreamConn, loaded := loadDownstreamConnection(ctx); loaded {
		logger.Infof(
			"inline request %s reusing cached downstream connection: %s",
			request.GetConnection().GetId(),
			connectionSummary(downstreamConn),
		)
	} else if trimDownstreamPath(request.GetConnection()) {
		logger.Infof(
			"inline request %s has no cached downstream connection; trimmed stale downstream path: %s",
			request.GetConnection().GetId(),
			connectionSummary(request.GetConnection()),
		)
	}

	originalConn := loadOriginalConnection(ctx)
	upstreamConn := request.GetConnection().Clone()
	closeCtxFunc := postpone.ContextWithValues(ctx)

	clientConn, err := c.client.Request(ctx, request, c.callOptions...)
	if err != nil {
		logrus.Errorf("inline downstream request failed: %s err=%v", requestSummary(request), err)
		logger.Errorf(
			"inline downstream request failed for %s: %v",
			connectionSummary(request.GetConnection()),
			err,
		)
		deleteOriginalConnection(ctx)
		return nil, err
	}
	storeDownstreamConnection(ctx, clientConn)
	logger.Infof(
		"inline downstream request established: upstream=%s downstream=%s",
		connectionSummary(upstreamConn),
		connectionSummary(clientConn),
	)

	responseConn := clientConn.Clone()
	if originalConn != nil && originalConn.GetMechanism() != nil {
		responseConn.Mechanism = originalConn.GetMechanism().Clone()
	} else {
		// The first upstream response must allocate its own local interface.
		// If we forward the downstream mechanism back into kernel.NewServer(),
		// the middle NSE reuses the downstream link and never gets a second
		// interface for a two-legged inline processor.
		responseConn.Mechanism = nil
	}
	if endpointName := upstreamConn.GetNetworkServiceEndpointName(); endpointName != "" {
		// Keep refreshes anchored on the middle NSE instead of letting them
		// jump straight to the cached downstream endpoint.
		responseConn.NetworkServiceEndpointName = endpointName
	}

	request.Connection = responseConn
	serverConn, serverErr := next.Server(ctx).Request(ctx, request)
	if serverErr != nil {
		logger.Errorf(
			"inline upstream request failed for %s after downstream=%s: %v",
			connectionSummary(request.GetConnection()),
			connectionSummary(clientConn),
			serverErr,
		)
		if sessionErr := closeProcessorSession(ctx); sessionErr != nil {
			logger.Errorf("inline processor cleanup failed after upstream request error: %v", sessionErr)
		}
		deleteOriginalConnection(ctx)
		_, _ = loadAndDeleteDownstreamConnection(ctx)
		closeCtx, closeCancel := closeCtxFunc()
		defer closeCancel()
		_, _ = c.client.Close(closeCtx, clientConn, c.callOptions...)
		return nil, serverErr
	}
	if endpointName := upstreamConn.GetNetworkServiceEndpointName(); endpointName != "" {
		serverConn.NetworkServiceEndpointName = endpointName
	}

	logrus.Infof("inline connect upstream response: %s", connectionSummary(serverConn))
	return serverConn, nil
}

func (c *connectServer) Close(ctx context.Context, conn *networkservice.Connection) (*emptypb.Empty, error) {
	var errs []error
	logger := log.FromContext(ctx)

	logger.Infof("inline close requested for %s", connectionSummary(conn))

	if err := closeProcessorSession(ctx); err != nil {
		errs = append(errs, err)
	}

	deleteOriginalConnection(ctx)

	downstreamConn, loaded := loadAndDeleteDownstreamConnection(ctx)
	if !loaded {
		logger.Warnf(
			"inline close %s has no cached downstream connection; falling back to upstream connection",
			conn.GetId(),
		)
		downstreamConn = conn
	} else {
		logger.Infof(
			"inline close %s using cached downstream connection: %s",
			conn.GetId(),
			connectionSummary(downstreamConn),
		)
	}

	if _, err := c.client.Close(ctx, downstreamConn, c.callOptions...); err != nil {
		errs = append(errs, fmt.Errorf("downstream close: %w", err))
	}
	if _, err := next.Server(ctx).Close(ctx, conn); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return nil, stderrors.Join(errs...)
	}
	return &emptypb.Empty{}, nil
}

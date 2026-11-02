package inline

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/networkservicemesh/api/pkg/api/networkservice"
	"github.com/networkservicemesh/sdk/pkg/networkservice/core/next"
	"github.com/networkservicemesh/sdk/pkg/tools/log"
)

type mechanismClient struct{}

func NewMechanismClient() networkservice.NetworkServiceClient {
	return &mechanismClient{}
}

func (c *mechanismClient) Request(ctx context.Context, request *networkservice.NetworkServiceRequest, opts ...grpc.CallOption) (*networkservice.Connection, error) {
	logger := log.FromContext(ctx)
	clientRequest := request.Clone()
	clientRequest.MechanismPreferences = nil

	if downstreamConn, ok := loadDownstreamConnection(ctx); ok && downstreamConn.GetMechanism() != nil {
		clientRequest.Connection.Mechanism = downstreamConn.GetMechanism().Clone()
		logger.Infof(
			"inline mechanism request %s reusing cached downstream mechanism: %s",
			request.GetConnection().GetId(),
			connectionSummary(downstreamConn),
		)
	} else {
		clientRequest.Connection.Mechanism = nil
		logger.Infof(
			"inline mechanism request %s has no cached downstream mechanism; forwarding without existing mechanism: %s",
			request.GetConnection().GetId(),
			connectionSummary(clientRequest.GetConnection()),
		)
	}

	return next.Client(ctx).Request(ctx, clientRequest, opts...)
}

func (c *mechanismClient) Close(ctx context.Context, conn *networkservice.Connection, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	return next.Client(ctx).Close(ctx, conn, opts...)
}

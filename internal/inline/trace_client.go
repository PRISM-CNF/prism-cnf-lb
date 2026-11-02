package inline

import (
	"context"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/networkservicemesh/api/pkg/api/networkservice"
	"github.com/networkservicemesh/sdk/pkg/networkservice/core/next"
)

type traceClient struct{}

func NewTraceClient() networkservice.NetworkServiceClient {
	return &traceClient{}
}

func (c *traceClient) Request(ctx context.Context, request *networkservice.NetworkServiceRequest, opts ...grpc.CallOption) (*networkservice.Connection, error) {
	logrus.Infof("inline trace outgoing downstream request: %s", requestSummary(request))
	conn, err := next.Client(ctx).Request(ctx, request, opts...)
	if err != nil {
		logrus.Errorf("inline trace downstream request failed: %s err=%v", requestSummary(request), err)
		return nil, err
	}
	logrus.Infof("inline trace downstream response: %s", connectionSummary(conn))
	return conn, nil
}

func (c *traceClient) Close(ctx context.Context, conn *networkservice.Connection, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	logrus.Infof("inline trace downstream close: %s", connectionSummary(conn))
	return next.Client(ctx).Close(ctx, conn, opts...)
}

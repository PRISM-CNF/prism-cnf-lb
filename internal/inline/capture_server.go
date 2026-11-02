package inline

import (
	"context"

	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/networkservicemesh/api/pkg/api/networkservice"
	"github.com/networkservicemesh/sdk/pkg/networkservice/core/next"
)

type captureServer struct{}

func NewCaptureServer() networkservice.NetworkServiceServer {
	return &captureServer{}
}

func (c *captureServer) Request(ctx context.Context, request *networkservice.NetworkServiceRequest) (*networkservice.Connection, error) {
	logrus.Infof("inline capture request: %s", requestSummary(request))
	storeOriginalConnection(ctx, request.GetConnection())
	return next.Server(ctx).Request(ctx, request)
}

func (c *captureServer) Close(ctx context.Context, conn *networkservice.Connection) (*emptypb.Empty, error) {
	deleteOriginalConnection(ctx)
	return next.Server(ctx).Close(ctx, conn)
}

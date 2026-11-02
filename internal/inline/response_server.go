package inline

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/networkservicemesh/api/pkg/api/networkservice"
	"github.com/networkservicemesh/sdk/pkg/networkservice/core/next"
	"github.com/networkservicemesh/sdk/pkg/tools/log"
	"github.com/networkservicemesh/sdk/pkg/tools/postpone"

	"nse-ovs-lb/internal/processor"
)

type responseServer struct {
	processor processor.Processor
}

func NewResponseServer(vnfProcessor processor.Processor) networkservice.NetworkServiceServer {
	return &responseServer{processor: vnfProcessor}
}

func (s *responseServer) Request(ctx context.Context, request *networkservice.NetworkServiceRequest) (*networkservice.Connection, error) {
	logger := log.FromContext(ctx)
	closeCtxFunc := postpone.ContextWithValues(ctx)

	logrus.Infof("inline response request entered: %s", requestSummary(request))
	conn, err := next.Server(ctx).Request(ctx, request)
	if err != nil {
		logrus.Errorf("inline response inner request failed: %s err=%v", requestSummary(request), err)
		return nil, err
	}
	logrus.Infof("inline response inner response: %s", connectionSummary(conn))

	downstreamConn, _ := loadDownstreamConnection(ctx)
	flow, err := flowForConnectionPath(conn, downstreamConn)
	if err != nil {
		logger.Errorf("inline flow build failed from response path for %s: %v", connectionSummary(conn), err)
		closeCtx, closeCancel := closeCtxFunc()
		defer closeCancel()
		_, _ = next.Server(ctx).Close(closeCtx, conn)
		return nil, err
	}
	if flow == nil {
		logger.Infof(
			"inline response skipping processor for %s because both legs resolve to one interface",
			connectionSummary(conn),
		)
		return conn, nil
	}

	logger.Infof(
		"inline flow resolved for %s: ingress=%s egress=%s",
		connectionSummary(conn),
		flow.IngressInterface,
		flow.EgressInterface,
	)

	session, err := s.processor.Activate(ctx, flow)
	if err != nil {
		if processor.IsPending(err) {
			logger.Warnf(
				"processor activation is pending for flow %s on %s -> %s: %v",
				flow.ConnectionID,
				flow.IngressInterface,
				flow.EgressInterface,
				err,
			)
			asyncCtx, asyncCancel := closeCtxFunc()
			go func() {
				defer asyncCancel()

				processorCtx, cancel := context.WithTimeout(asyncCtx, 30*time.Second)
				defer cancel()

				asyncSession, asyncErr := s.processor.Activate(processorCtx, flow.Clone())
				if asyncErr != nil {
					logger.Errorf(
						"deferred processor activation failed for flow %s on %s -> %s: %v",
						flow.ConnectionID,
						flow.IngressInterface,
						flow.EgressInterface,
						asyncErr,
					)
					return
				}
				storeProcessorSession(asyncCtx, asyncSession)
			}()
			return conn, nil
		}

		logger.Errorf(
			"processor activation failed for flow %s on %s -> %s: %v",
			flow.ConnectionID,
			flow.IngressInterface,
			flow.EgressInterface,
			err,
		)
		closeCtx, closeCancel := closeCtxFunc()
		defer closeCancel()
		_, _ = next.Server(ctx).Close(closeCtx, conn)
		return nil, fmt.Errorf("activate processor for flow %s: %w", flow.ConnectionID, err)
	}

	storeProcessorSession(ctx, session)
	return conn, nil
}

func (s *responseServer) Close(ctx context.Context, conn *networkservice.Connection) (*emptypb.Empty, error) {
	return next.Server(ctx).Close(ctx, conn)
}

package inline

import (
	"context"

	"github.com/networkservicemesh/api/pkg/api/networkservice"
	nsmetadata "github.com/networkservicemesh/sdk/pkg/networkservice/utils/metadata"

	"nse-ovs-lb/internal/processor"
)

type originalConnectionKey struct{}
type downstreamConnectionKey struct{}
type processorSessionKey struct{}

func storeOriginalConnection(ctx context.Context, conn *networkservice.Connection) {
	if conn == nil {
		return
	}
	nsmetadata.Map(ctx, false).Store(originalConnectionKey{}, conn.Clone())
}

func loadOriginalConnection(ctx context.Context) *networkservice.Connection {
	rawValue, ok := nsmetadata.Map(ctx, false).Load(originalConnectionKey{})
	if !ok {
		return nil
	}
	conn, ok := rawValue.(*networkservice.Connection)
	if !ok || conn == nil {
		return nil
	}
	return conn.Clone()
}

func deleteOriginalConnection(ctx context.Context) {
	_, _ = nsmetadata.Map(ctx, false).LoadAndDelete(originalConnectionKey{})
}

func storeDownstreamConnection(ctx context.Context, conn *networkservice.Connection) {
	if conn == nil {
		return
	}
	nsmetadata.Map(ctx, false).Store(downstreamConnectionKey{}, conn.Clone())
}

func loadDownstreamConnection(ctx context.Context) (*networkservice.Connection, bool) {
	rawValue, ok := nsmetadata.Map(ctx, false).Load(downstreamConnectionKey{})
	if !ok {
		return nil, false
	}
	conn, ok := rawValue.(*networkservice.Connection)
	if !ok || conn == nil {
		return nil, false
	}
	return conn.Clone(), true
}

func loadAndDeleteDownstreamConnection(ctx context.Context) (*networkservice.Connection, bool) {
	rawValue, ok := nsmetadata.Map(ctx, false).LoadAndDelete(downstreamConnectionKey{})
	if !ok {
		return nil, false
	}
	conn, ok := rawValue.(*networkservice.Connection)
	if !ok || conn == nil {
		return nil, false
	}
	return conn.Clone(), true
}

func storeProcessorSession(ctx context.Context, session processor.Session) {
	if session == nil {
		return
	}
	nsmetadata.Map(ctx, false).Store(processorSessionKey{}, session)
}

func loadAndDeleteProcessorSession(ctx context.Context) (processor.Session, bool) {
	rawValue, ok := nsmetadata.Map(ctx, false).LoadAndDelete(processorSessionKey{})
	if !ok {
		return nil, false
	}
	session, ok := rawValue.(processor.Session)
	if !ok || session == nil {
		return nil, false
	}
	return session, true
}

func closeProcessorSession(ctx context.Context) error {
	session, ok := loadAndDeleteProcessorSession(ctx)
	if !ok {
		return nil
	}
	return session.Close(ctx)
}

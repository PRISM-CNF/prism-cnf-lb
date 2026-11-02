//go:build linux
// +build linux

package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/tap"
)

type grpcServerStatsHandler struct{}

func newGRPCServerStatsHandler() stats.Handler {
	return &grpcServerStatsHandler{}
}

func (h *grpcServerStatsHandler) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	transportDebugf("grpc stats rpc tagged: method=%s", info.FullMethodName)
	return ctx
}

func (h *grpcServerStatsHandler) HandleRPC(_ context.Context, rpcStats stats.RPCStats) {
	switch event := rpcStats.(type) {
	case *stats.Begin:
		transportDebugf(
			"grpc stats rpc begin: client=%t method_streams=%t/%t",
			event.Client,
			event.IsClientStream,
			event.IsServerStream,
		)
	case *stats.InHeader:
		transportDebugf(
			"grpc stats in-header: method=%s remote=%v local=%v metadata=%s",
			event.FullMethod,
			event.RemoteAddr,
			event.LocalAddr,
			summarizeMetadata(event.Header),
		)
	case *stats.InPayload:
		transportDebugf(
			"grpc stats in-payload: payload_type=%T length=%d compressed_length=%d wire_length=%d",
			event.Payload,
			event.Length,
			event.CompressedLength,
			event.WireLength,
		)
	case *stats.OutHeader:
		transportDebugf("grpc stats out-header: metadata=%s", summarizeMetadata(event.Header))
	case *stats.OutPayload:
		transportDebugf(
			"grpc stats out-payload: payload_type=%T length=%d compressed_length=%d wire_length=%d",
			event.Payload,
			event.Length,
			event.CompressedLength,
			event.WireLength,
		)
	case *stats.End:
		transportDebugf("grpc stats rpc end: err=%v", event.Error)
	}
}

func (h *grpcServerStatsHandler) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	transportDebugf("grpc stats conn tagged: remote=%v local=%v", info.RemoteAddr, info.LocalAddr)
	return ctx
}

func (h *grpcServerStatsHandler) HandleConn(_ context.Context, connStats stats.ConnStats) {
	switch event := connStats.(type) {
	case *stats.ConnBegin:
		transportDebugf("grpc stats conn begin: client=%t", event.Client)
	case *stats.ConnEnd:
		transportDebugf("grpc stats conn end: client=%t", event.Client)
	}
}

func grpcRequestTapHandle(ctx context.Context, info *tap.Info) (context.Context, error) {
	transportDebugf(
		"grpc tap request received: method=%s metadata=%s",
		info.FullMethodName,
		summarizeMetadata(info.Header),
	)
	return ctx, nil
}

func summarizeMetadata(md metadata.MD) string {
	if len(md) == 0 {
		return "{}"
	}

	keys := make([]string, 0, len(md))
	for key := range md {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		values := md.Get(key)
		if strings.EqualFold(key, "authorization") {
			parts = append(parts, fmt.Sprintf("%s=[redacted:%d]", key, len(values)))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%d", key, len(values)))
	}

	return "{" + strings.Join(parts, ", ") + "}"
}

func transportDebugf(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	logrus.Info(message)
	_, _ = fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().Format(time.RFC3339Nano), message)
}

func grpcOutboundUnaryInterceptor(
	ctx context.Context,
	method string,
	req interface{},
	reply interface{},
	cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption,
) error {
	transportDebugf(
		"grpc client unary request start: method=%s target=%s deadline=%s req_type=%T",
		method,
		cc.Target(),
		summarizeDeadline(ctx),
		req,
	)

	err := invoker(ctx, method, req, reply, cc, opts...)
	if err != nil {
		transportDebugf(
			"grpc client unary request failed: method=%s target=%s err=%v",
			method,
			cc.Target(),
			err,
		)
		return err
	}

	transportDebugf(
		"grpc client unary request completed: method=%s target=%s reply_type=%T",
		method,
		cc.Target(),
		reply,
	)
	return nil
}

func summarizeDeadline(ctx context.Context) string {
	deadline, ok := ctx.Deadline()
	if !ok {
		return "<none>"
	}
	return deadline.Format(time.RFC3339Nano)
}

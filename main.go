//go:build linux
// +build linux

package main

import (
	"context"
	"crypto/tls"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	nested "github.com/antonfisher/nested-logrus-formatter"
	"github.com/edwarnicke/grpcfd"
	"github.com/sirupsen/logrus"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/networkservicemesh/api/pkg/api/networkservice"
	kernelmech "github.com/networkservicemesh/api/pkg/api/networkservice/mechanisms/kernel"
	"github.com/networkservicemesh/sdk/pkg/networkservice/chains/client"
	"github.com/networkservicemesh/sdk/pkg/networkservice/chains/endpoint"
	"github.com/networkservicemesh/sdk/pkg/networkservice/common/authorize"
	"github.com/networkservicemesh/sdk/pkg/networkservice/common/clienturl"
	"github.com/networkservicemesh/sdk/pkg/networkservice/common/mechanisms"
	kernel "github.com/networkservicemesh/sdk/pkg/networkservice/common/mechanisms/kernel"
	"github.com/networkservicemesh/sdk/pkg/networkservice/common/mechanisms/recvfd"
	"github.com/networkservicemesh/sdk/pkg/networkservice/common/mechanisms/sendfd"
	"github.com/networkservicemesh/sdk/pkg/networkservice/common/passthrough"
	"github.com/networkservicemesh/sdk/pkg/networkservice/core/chain"
	"github.com/networkservicemesh/sdk/pkg/networkservice/utils/metadata"
	"github.com/networkservicemesh/sdk/pkg/tools/debug"
	"github.com/networkservicemesh/sdk/pkg/tools/log"
	"github.com/networkservicemesh/sdk/pkg/tools/log/logruslogger"
	"github.com/networkservicemesh/sdk/pkg/tools/opentelemetry"
	"github.com/networkservicemesh/sdk/pkg/tools/pprofutils"
	"github.com/networkservicemesh/sdk/pkg/tools/spiffejwt"
	"github.com/networkservicemesh/sdk/pkg/tools/token"
	"github.com/networkservicemesh/sdk/pkg/tools/tracing"

	"nse-ovs-lb/internal"
	"nse-ovs-lb/internal/inline"
	"nse-ovs-lb/internal/processor"
	"nse-ovs-lb/internal/vnf"
)

func main() {
	ctx, cancel := notifyContext()
	defer cancel()

	log.EnableTracing(true)
	logrus.SetFormatter(&nested.Formatter{})
	ctx = log.WithLog(ctx, logruslogger.New(ctx, map[string]interface{}{"cmd": os.Args[0]}))

	if err := debug.Self(); err != nil {
		log.FromContext(ctx).Infof("%s", err)
	}

	startTime := time.Now()
	log.FromContext(ctx).Info("starting OVS inline LB NSE")
	transportDebugf("transport debug enabled")

	config, err := internal.LoadConfig(ctx)
	if err != nil {
		logrus.Fatal(err.Error())
	}

	level, err := logrus.ParseLevel(config.LogLevel)
	if err != nil {
		logrus.Fatalf("invalid log level %q: %v", config.LogLevel, err)
	}
	logrus.SetLevel(level)

	logruslogger.SetupLevelChangeOnSignal(ctx, map[os.Signal]logrus.Level{
		syscall.SIGUSR1: logrus.TraceLevel,
		syscall.SIGUSR2: level,
	})

	if opentelemetry.IsEnabled() {
		collectorAddress := config.OpenTelemetryEndpoint
		spanExporter := opentelemetry.InitSpanExporter(ctx, collectorAddress)
		metricExporter := opentelemetry.InitOPTLMetricExporter(ctx, collectorAddress, config.MetricsExportInterval)
		otelCloser := opentelemetry.Init(ctx, spanExporter, metricExporter, config.Name)
		defer func() {
			if closeErr := otelCloser.Close(); closeErr != nil {
				log.FromContext(ctx).Error(closeErr.Error())
			}
		}()
	}

	if config.PprofEnabled {
		go pprofutils.ListenAndServe(ctx, config.PprofListenOn)
	}

	source, err := workloadapi.NewX509Source(ctx)
	if err != nil {
		logrus.Fatalf("failed to create X509 source: %+v", err)
	}
	defer source.Close()

	svid, err := source.GetX509SVID()
	if err != nil {
		logrus.Fatalf("failed to read X509 SVID: %+v", err)
	}
	log.FromContext(ctx).Infof("SPIFFE identity: %q", svid.ID)

	tlsClientConfig := tlsconfig.MTLSClientConfig(source, source, tlsconfig.AuthorizeAny())
	tlsClientConfig.MinVersion = tls.VersionTLS12

	tlsServerConfig := tlsconfig.MTLSServerConfig(source, source, tlsconfig.AuthorizeAny())
	tlsServerConfig.MinVersion = tls.VersionTLS12

	clientOptions := append(
		tracing.WithTracingDial(),
		grpc.WithDefaultCallOptions(
			grpc.WaitForReady(true),
			grpc.PerRPCCredentials(token.NewPerRPCCredentials(
				spiffejwt.TokenGeneratorFunc(source, config.MaxTokenLifetime),
			)),
		),
		grpc.WithChainUnaryInterceptor(grpcOutboundUnaryInterceptor),
		grpc.WithTransportCredentials(
			grpcfd.TransportCredentials(
				credentials.NewTLS(tlsClientConfig),
			),
		),
		grpcfd.WithChainStreamInterceptor(),
		grpcfd.WithChainUnaryInterceptor(),
	)

	downstreamFunctionality := []networkservice.NetworkServiceClient{
		passthrough.NewClient(config.DownstreamLabels),
		inline.NewMechanismClient(),
		kernel.NewClient(),
		sendfd.NewClient(),
		recvfd.NewClient(),
		inline.NewTraceClient(),
	}

	downstreamClient := client.NewClient(
		ctx,
		client.WithoutRefresh(),
		client.WithName(config.Name),
		client.WithDialOptions(clientOptions...),
		client.WithAdditionalFunctionality(
			append(
				[]networkservice.NetworkServiceClient{metadata.NewClient()},
				downstreamFunctionality...,
			)...,
		),
	)

	// The LB-specific VNF logic lives under internal/vnf.
	vnfProcessor := processor.New(vnf.New(vnf.Options{
		RulesPath:        config.LBRulesPath,
		RulePollInterval: config.LBRulePollInterval,
	}))
	lbEndpoint := endpoint.NewServer(
		ctx,
		spiffejwt.TokenGeneratorFunc(source, config.MaxTokenLifetime),
		endpoint.WithName(config.Name),
		endpoint.WithAuthorizeServer(authorize.NewServer()),
		endpoint.WithAdditionalFunctionality(
			recvfd.NewServer(),
			sendfd.NewServer(),
			clienturl.NewServer(&config.ConnectTo),
			inline.NewCaptureServer(),
			inline.NewConnectServer(downstreamClient),
			inline.NewResponseServer(vnfProcessor),
			mechanisms.NewServer(map[string]networkservice.NetworkServiceServer{
				kernelmech.MECHANISM: chain.NewNetworkServiceServer(
					kernel.NewServer(),
				),
			}),
		),
	)

	server := grpc.NewServer(append(
		tracing.WithTracing(),
		grpc.StatsHandler(newGRPCServerStatsHandler()),
		grpc.InTapHandle(grpcRequestTapHandle),
		grpc.ChainUnaryInterceptor(grpcRequestLogInterceptor),
		grpc.Creds(
			grpcfd.TransportCredentials(
				credentials.NewTLS(tlsServerConfig),
			),
		),
	)...)

	lbEndpoint.Register(server)

	listenOn, registerOn, cleanupListen, err := prepareListenURLs(config.Name, config.ListenOn, os.Getenv("POD_IP"))
	if err != nil {
		logrus.Fatalf("failed to prepare listen URL: %+v", err)
	}
	defer cleanupListen()

	serverErrCh := listenAndServeWithDebug(ctx, listenOn, server)
	exitOnErr(ctx, cancel, serverErrCh)

	nseRegistryClient := internal.NewRegistryClient(ctx, &config.ConnectTo, clientOptions, config.RegistryClientPolicies)
	nse, err := internal.RegisterEndpoint(
		ctx,
		nseRegistryClient,
		config.Name,
		config.ServiceName,
		config.Labels,
		registerOn.String(),
	)
	if err != nil {
		log.FromContext(ctx).Fatalf("failed to register endpoint: %+v", err)
	}

	log.FromContext(ctx).Infof("registered endpoint %s for service %v at %s", nse.Name, nse.NetworkServiceNames, nse.Url)
	log.FromContext(ctx).Infof("startup finished in %v", time.Since(startTime))

	<-ctx.Done()
	log.FromContext(ctx).Info("inline LB NSE stopped")
}

func prepareListenURLs(name string, rawListenOn string, podIP string) (*url.URL, *url.URL, func(), error) {
	if strings.Contains(rawListenOn, "://") {
		listenOn, err := url.Parse(rawListenOn)
		if err != nil {
			return nil, nil, nil, err
		}

		registerOn := cloneURL(listenOn)
		if listenOn.Scheme == "tcp" && podIP != "" {
			host, port, err := net.SplitHostPort(listenOn.Host)
			if err == nil && shouldAdvertisePodIP(host) {
				registerOn.Host = net.JoinHostPort(podIP, port)
			}
		}

		return listenOn, registerOn, func() {}, nil
	}

	tmpDir, err := os.MkdirTemp("", name)
	if err != nil {
		return nil, nil, nil, err
	}

	listenOn := &url.URL{
		Scheme: "unix",
		Path:   filepath.Join(tmpDir, rawListenOn),
	}

	return listenOn, cloneURL(listenOn), func() {
		_ = os.RemoveAll(tmpDir)
	}, nil
}

func cloneURL(src *url.URL) *url.URL {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}

func shouldAdvertisePodIP(host string) bool {
	switch host {
	case "", "0.0.0.0", "::", "[::]", "localhost":
		return true
	default:
		return false
	}
}

func exitOnErr(ctx context.Context, cancel context.CancelFunc, errCh <-chan error) {
	select {
	case err := <-errCh:
		log.FromContext(ctx).Fatal(err)
	default:
	}

	go func() {
		err := <-errCh
		if err == nil {
			return
		}
		log.FromContext(ctx).Error(err)
		cancel()
	}()
}

func notifyContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
		syscall.SIGQUIT,
	)
}

func grpcRequestLogInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	transportDebugf("grpc unary request entered: method=%s req_type=%T", info.FullMethod, req)
	resp, err := handler(ctx, req)
	if err != nil {
		transportDebugf("grpc unary request failed: method=%s err=%v", info.FullMethod, err)
		return nil, err
	}
	transportDebugf("grpc unary request completed: method=%s resp_type=%T", info.FullMethod, resp)
	return resp, nil
}

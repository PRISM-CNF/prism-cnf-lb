//go:build linux
// +build linux

package internal

import (
	"context"
	"net/url"

	registryapi "github.com/networkservicemesh/api/pkg/api/registry"
	registryclient "github.com/networkservicemesh/sdk/pkg/registry/chains/client"
	registryauthorize "github.com/networkservicemesh/sdk/pkg/registry/common/authorize"
	"github.com/networkservicemesh/sdk/pkg/registry/common/clientinfo"
	registrysendfd "github.com/networkservicemesh/sdk/pkg/registry/common/sendfd"
	"google.golang.org/grpc"
)

func NewRegistryClient(
	ctx context.Context,
	connectTo *url.URL,
	clientOptions []grpc.DialOption,
	registryPolicies []string,
) registryapi.NetworkServiceEndpointRegistryClient {
	opts := []registryclient.Option{
		registryclient.WithClientURL(connectTo),
		registryclient.WithDialOptions(clientOptions...),
		registryclient.WithNSEAdditionalFunctionality(
			clientinfo.NewNetworkServiceEndpointRegistryClient(),
			registrysendfd.NewNetworkServiceEndpointRegistryClient(),
		),
	}

	if len(registryPolicies) > 0 {
		opts = append(opts, registryclient.WithAuthorizeNSERegistryClient(
			registryauthorize.NewNetworkServiceEndpointRegistryClient(
				registryauthorize.WithPolicies(registryPolicies...),
			),
		))
	}

	return registryclient.NewNetworkServiceEndpointRegistryClient(ctx, opts...)
}

func RegisterEndpoint(
	ctx context.Context,
	client registryapi.NetworkServiceEndpointRegistryClient,
	name string,
	serviceName string,
	labels map[string]string,
	listenURL string,
) (*registryapi.NetworkServiceEndpoint, error) {
	return client.Register(ctx, &registryapi.NetworkServiceEndpoint{
		Name:                name,
		NetworkServiceNames: []string{serviceName},
		NetworkServiceLabels: map[string]*registryapi.NetworkServiceLabels{
			serviceName: {
				Labels: labels,
			},
		},
		Url: listenURL,
	})
}

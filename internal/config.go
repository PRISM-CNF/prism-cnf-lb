//go:build linux
// +build linux

package internal

import (
	"context"
	"net/url"
	"os"
	"time"

	"github.com/kelseyhightower/envconfig"
	"github.com/pkg/errors"
)

type Config struct {
	Name                   string            `default:"nse-ovs-lb" desc:"Name of inline LB NSE"`
	ListenOn               string            `default:"listen.on.sock" desc:"listen on socket" split_words:"true"`
	ConnectTo              url.URL           `default:"unix:///var/lib/networkservicemesh/nsm.io.sock" desc:"url to connect to" split_words:"true"`
	MaxTokenLifetime       time.Duration     `default:"10m" desc:"maximum lifetime of tokens" split_words:"true"`
	RegistryClientPolicies []string          `default:"" desc:"paths to files and directories that contain registry client policies" split_words:"true"`
	ServiceName            string            `default:"nse-ovs-lb" desc:"name of provided network service" split_words:"true"`
	Labels                 map[string]string `default:"app:lb" desc:"endpoint labels"`
	DownstreamLabels       map[string]string `default:"" desc:"labels used to select downstream endpoints; empty preserves the incoming request labels" split_words:"true"`
	LBRulesPath            string            `default:"/etc/nse-lb/rules.yaml" desc:"path to the load balancer rules YAML file" split_words:"true"`
	LBRulePollInterval     time.Duration     `default:"2s" desc:"interval for polling the LB rules file for live reload" split_words:"true"`
	LogLevel               string            `default:"INFO" desc:"log level" split_words:"true"`
	OpenTelemetryEndpoint  string            `default:"otel-collector.observability.svc.cluster.local:4317" desc:"OpenTelemetry Collector Endpoint" split_words:"true"`
	MetricsExportInterval  time.Duration     `default:"10s" desc:"interval between metrics exports" split_words:"true"`
	PprofEnabled           bool              `default:"false" desc:"is pprof enabled" split_words:"true"`
	PprofListenOn          string            `default:"localhost:6060" desc:"pprof URL to ListenAndServe" split_words:"true"`
}

func LoadConfig(_ context.Context) (*Config, error) {
	config := new(Config)

	if err := envconfig.Usage("nsm", config); err != nil {
		return nil, errors.Wrap(err, "cannot show usage of envconfig nsm")
	}

	if err := envconfig.Process("nsm", config); err != nil {
		return nil, errors.Wrap(err, "cannot process envconfig nsm")
	}

	if config.ServiceName == "" {
		config.ServiceName = config.Name
	}

	if config.Labels == nil {
		config.Labels = map[string]string{}
	}

	if len(config.DownstreamLabels) == 0 && len(config.Labels) > 0 {
		config.DownstreamLabels = make(map[string]string, len(config.Labels))
		for key, value := range config.Labels {
			config.DownstreamLabels[key] = value
		}
	}

	if err := requireReadableFile(config.LBRulesPath); err != nil {
		return nil, errors.Wrapf(err, "cannot load lb rules from %s", config.LBRulesPath)
	}
	if config.LBRulePollInterval <= 0 {
		return nil, errors.New("lb rule poll interval must be positive")
	}

	return config, nil
}

func requireReadableFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.Errorf("%s is a directory", path)
	}
	return nil
}

//go:build !notunnel

package tunnel

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"runtime"
	"time"

	"github.com/atticus6/go-vless/internal/i18n"
	"github.com/cloudflare/cloudflared/config"
	"github.com/cloudflare/cloudflared/connection"
	"github.com/cloudflare/cloudflared/edgediscovery"
	"github.com/cloudflare/cloudflared/edgediscovery/allregions"
	"github.com/cloudflare/cloudflared/features"
	"github.com/cloudflare/cloudflared/ingress"
	"github.com/cloudflare/cloudflared/logger"
	"github.com/cloudflare/cloudflared/metrics"
	"github.com/cloudflare/cloudflared/orchestration"
	"github.com/cloudflare/cloudflared/signal"
	"github.com/cloudflare/cloudflared/supervisor"
	"github.com/cloudflare/cloudflared/tlsconfig"
	"github.com/cloudflare/cloudflared/tunnelrpc/pogs"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/urfave/cli/v2"
)

// 隧道配置常量
const (
	gracePeriod         = 30
	haConnections       = 2
	maxRetries          = 5
	maxEdgeAddrRetries  = 8
	rpcTimeout          = 5 * time.Second
	quicConnFlowLimit   = 30 * (1 << 20) // 30MB
	quicStreamFlowLimit = 6 * (1 << 20)  // 6MB
)

// CreateCloudflareTunnel 创建并启动 Cloudflare 隧道.
// protocol: auto | quic | http2 (对应官方 --protocol, 默认 auto).
func CreateCloudflareTunnel(ctx context.Context, port int, protocol string) (string, error) {
	switch protocol {
	case "", "auto", "quic", "http2":
		if protocol == "" {
			protocol = "auto"
		}
	default:
		return "", fmt.Errorf("%s", i18n.Sprintf("tunnel.bad_proto", protocol))
	}
	metrics.RegisterBuildInfo(BuildType, BuildTime, Version)

	clientID, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("can't generate connector UUID: %w", err)
	}

	logTransport := logger.Create(logger.CreateConfig("", false, "", ""))
	observer := connection.NewObserver(logTransport, logTransport)

	ing, err := ingress.ParseIngress(&config.Configuration{
		Ingress: []config.UnvalidatedIngressRule{
			{Service: fmt.Sprintf("http://localhost:%d", port)},
		},
	})
	if err != nil {
		return "", fmt.Errorf("can't parse ingress: %w", err)
	}

	orchestrator, err := orchestration.NewOrchestrator(
		ctx,
		&orchestration.Config{
			Ingress:            &ing,
			WarpRouting:        ingress.NewWarpRoutingConfig(&config.WarpRoutingConfig{}),
			ConfigurationFlags: map[string]string{},
			WriteTimeout:       0,
		},
		[]pogs.Tag{},
		[]ingress.Rule{},
		logTransport,
	)
	if err != nil {
		return "", fmt.Errorf("can't create orchestrator: %w", err)
	}

	protocolSelector, err := connection.NewProtocolSelector(
		protocol,
		"random value",
		false,
		false,
		edgediscovery.ProtocolPercentage,
		connection.ResolveTTL,
		logTransport,
	)
	if err != nil {
		return "", fmt.Errorf("unable to create protocol selector: %w", err)
	}

	edgeTLSConfigs, err := buildEdgeTLSConfigs()
	if err != nil {
		return "", err
	}

	tunnel, err := createTunnel(clientID)
	if err != nil {
		return "", err
	}

	tunnelConfig := buildTunnelConfig(clientID, tunnel, observer, logTransport, protocolSelector, edgeTLSConfigs)

	connectedSignal := signal.New(make(chan struct{}))
	reconnectCh := make(chan supervisor.ReconnectSignal, 4)
	shutdown := make(chan struct{})

	go func() {
		if err := supervisor.StartTunnelDaemon(ctx, tunnelConfig, orchestrator, connectedSignal, reconnectCh, shutdown); err != nil {
			panic(fmt.Errorf("failed to start tunnel daemon: %w", err))
		}
	}()

	return "https://" + tunnel.QuickTunnelUrl, nil
}

func buildEdgeTLSConfigs() (map[connection.Protocol]*tls.Config, error) {
	configs := make(map[connection.Protocol]*tls.Config, len(connection.ProtocolList))
	cliCtx := cli.NewContext(cli.NewApp(), &flag.FlagSet{}, nil)

	for _, p := range connection.ProtocolList {
		tlsSettings := p.TLSSettings()
		if tlsSettings == nil {
			return nil, fmt.Errorf("%s has unknown TLS settings", p)
		}

		tlsConfig, err := tlsconfig.CreateTunnelConfig(cliCtx, tlsSettings.ServerName)
		if err != nil {
			return nil, fmt.Errorf("unable to create TLS config: %w", err)
		}

		if len(tlsSettings.NextProtos) > 0 {
			tlsConfig.NextProtos = tlsSettings.NextProtos
		}
		configs[p] = tlsConfig
	}
	return configs, nil
}

func buildTunnelConfig(
	clientID uuid.UUID,
	tunnel *connection.TunnelProperties,
	observer *connection.Observer,
	logTransport *zerolog.Logger,
	protocolSelector connection.ProtocolSelector,
	edgeTLSConfigs map[connection.Protocol]*tls.Config,
) *supervisor.TunnelConfig {
	return &supervisor.TunnelConfig{
		GracePeriod:                         gracePeriod,
		ReplaceExisting:                     false,
		OSArch:                              runtime.GOOS + "_" + runtime.GOARCH,
		ClientID:                            clientID.String(),
		EdgeAddrs:                           []string{},
		Region:                              "",
		EdgeIPVersion:                       allregions.Auto,
		EdgeBindAddr:                        nil,
		HAConnections:                       haConnections,
		IsAutoupdated:                       false,
		LBPool:                              "",
		Tags:                                []pogs.Tag{},
		Log:                                 logTransport,
		LogTransport:                        logTransport,
		Observer:                            observer,
		ReportedVersion:                     "embedded-go-test",
		Retries:                             maxRetries,
		RunFromTerminal:                     true,
		NamedTunnel:                         tunnel,
		ProtocolSelector:                    protocolSelector,
		EdgeTLSConfigs:                      edgeTLSConfigs,
		FeatureSelector:                     &features.FeatureSelector{},
		MaxEdgeAddrRetries:                  maxEdgeAddrRetries,
		RPCTimeout:                          rpcTimeout,
		WriteStreamTimeout:                  0,
		DisableQUICPathMTUDiscovery:         false,
		QUICConnectionLevelFlowControlLimit: quicConnFlowLimit,
		QUICStreamLevelFlowControlLimit:     quicStreamFlowLimit,
		ICMPRouterServer:                    nil,
	}
}

package xrayrt

import (
	"testing"

	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/app/proxyman"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	featureoutbound "github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/proxy/blackhole"
	"github.com/xtls/xray-core/proxy/freedom"
)

func newRunningTestInstance(t *testing.T) *core.Instance {
	t.Helper()
	instance, err := core.New(baseCoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	return instance
}

func baseCoreConfig() *core.Config {
	return &core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
		},
		Outbound: []*core.OutboundHandlerConfig{{
			Tag:           "blocked-default",
			ProxySettings: serial.ToTypedMessage(&blackhole.Config{}),
		}},
	}
}

func freedomOutbound(tag, redirect string) *core.OutboundHandlerConfig {
	config := &freedom.Config{
		FinalRules: []*freedom.FinalRuleConfig{{Action: freedom.RuleAction_Allow}},
	}
	if redirect != "" {
		destination, _ := xnet.ParseDestination("tcp:" + redirect)
		config.DestinationOverride = &freedom.DestinationOverride{Server: &protocol.ServerEndpoint{
			Address: xnet.NewIPOrDomain(destination.Address),
			Port:    uint32(destination.Port),
		}}
	}
	return &core.OutboundHandlerConfig{Tag: tag, ProxySettings: serial.ToTypedMessage(config)}
}

func outboundManager(instance *core.Instance) featureoutbound.Manager {
	return instance.GetFeature(featureoutbound.ManagerType()).(featureoutbound.Manager)
}

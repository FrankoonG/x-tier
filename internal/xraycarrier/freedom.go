package xraycarrier

import (
	"context"
	"net"

	rendrxray "github.com/FrankoonG/rendr/xray"
	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/proxy/freedom"
	"google.golang.org/protobuf/proto"

	_ "github.com/xtls/xray-core/app/dispatcher"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	_ "github.com/xtls/xray-core/proxy/freedom"
)

type Dialer struct {
	inst *core.Instance
}

func NewFreedomDialer() (*Dialer, error) {
	cfg := &core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
		},
		Outbound: []*core.OutboundHandlerConfig{
			{
				ProxySettings: serial.ToTypedMessage(&freedom.Config{
					FinalRules: []*freedom.FinalRuleConfig{{Action: freedom.RuleAction_Allow}},
				}),
			},
		},
	}
	b, err := proto.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	inst, err := core.StartInstance("protobuf", b)
	if err != nil {
		return nil, err
	}
	return &Dialer{inst: inst}, nil
}

func (d *Dialer) Dial(ctx context.Context, addr string) (net.Conn, error) {
	return rendrxray.XrayInstanceAsStreamFactory(d.inst)(ctx, addr)
}

func (d *Dialer) Close() {
	if d != nil && d.inst != nil {
		d.inst.Close()
	}
}

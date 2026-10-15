package xray

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	handler "github.com/xtls/xray-core/app/proxyman/command"
	router "github.com/xtls/xray-core/app/router/command"
	stats "github.com/xtls/xray-core/app/stats/command"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	confserial "github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Embedded owns the core and one persistent API connection in the gateway
// process. Command retains the existing transaction/readback contract without
// starting a second Go runtime for each handler or routing operation.
type APIClient struct {
	conn   *grpc.ClientConn
	server string
}

type Embedded struct {
	*APIClient
	core *core.Instance
}

func StartEmbedded(config []byte, server string) (*Embedded, error) {
	instance, err := core.StartInstance("json", config)
	if err != nil {
		return nil, err
	}
	client, err := NewAPIClient(server)
	if err != nil {
		instance.Close()
		return nil, err
	}
	return &Embedded{core: instance, APIClient: client}, nil
}

// NewAPIClient shares a bounded, persistent control connection for either core
// execution model. A process comparison must not reintroduce CLI subprocesses.
func NewAPIClient(server string) (*APIClient, error) {
	conn, err := grpc.NewClient(server, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(32<<20), grpc.MaxCallSendMsgSize(32<<20)))
	if err != nil {
		return nil, err
	}
	return &APIClient{conn: conn, server: server}, nil
}

func (e *APIClient) Close() error { return e.conn.Close() }
func (e *Embedded) Close() error  { return errors.Join(e.APIClient.Close(), e.core.Close()) }

func (e *APIClient) Command(parent context.Context, _ string, args ...string) ([]byte, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if len(args) < 2 || args[0] != "api" {
		return nil, errors.New("embedded API requires an action")
	}
	flags := flag.NewFlagSet(args[1], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	server := e.server
	flags.StringVar(&server, "server", e.server, "")
	flags.StringVar(&server, "s", e.server, "")
	timeout := 5
	flags.IntVar(&timeout, "timeout", 5, "")
	flags.IntVar(&timeout, "t", 5, "")
	balancer := flags.String("b", "", "")
	email := flags.String("email", "", "")
	appendRules := flags.Bool("append", false, "")
	if err := flags.Parse(args[2:]); err != nil {
		return nil, err
	}
	if server != e.server || timeout <= 0 || timeout > 600 {
		return nil, errors.New("invalid embedded API endpoint or timeout")
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeout)*time.Second)
	defer cancel()
	rest := flags.Args()
	routing := router.NewRoutingServiceClient(e.conn)
	handlers := handler.NewHandlerServiceClient(e.conn)
	switch args[1] {
	case "bi":
		if len(rest) != 1 {
			return nil, errors.New("balancer tag required")
		}
		result, err := routing.GetBalancerInfo(ctx, &router.GetBalancerInfoRequest{Tag: rest[0]})
		if err != nil {
			return nil, err
		}
		target := result.GetBalancer().GetOverride().GetTarget()
		if target == "" {
			return []byte("  - Selects:\n"), nil
		}
		return []byte(fmt.Sprintf("  - Selecting Override:\n    1   %s\n  - Selects:\n", target)), nil
	case "bo":
		if *balancer == "" || len(rest) != 1 {
			return nil, errors.New("balancer and target required")
		}
		_, err := routing.OverrideBalancerTarget(ctx, &router.OverrideBalancerTargetRequest{BalancerTag: *balancer, Target: rest[0]})
		return nil, err
	case "lso":
		result, err := handlers.ListOutbounds(ctx, &handler.ListOutboundsRequest{})
		if err != nil {
			return nil, err
		}
		// Project tags only. Never return credentials to callers of inventory.
		tags := make([]map[string]string, 0, len(result.Outbounds))
		for _, out := range result.Outbounds {
			tags = append(tags, map[string]string{"tag": out.Tag})
		}
		return json.Marshal(map[string]any{"outbounds": tags})
	case "rmo":
		if len(rest) != 1 {
			return nil, errors.New("outbound tag required")
		}
		_, err := handlers.RemoveOutbound(ctx, &handler.RemoveOutboundRequest{Tag: rest[0]})
		return nil, err
	case "ado", "adrules":
		if len(rest) != 1 {
			return nil, errors.New("one configuration file required")
		}
		f, err := os.Open(rest[0])
		if err != nil {
			return nil, err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 32<<20 {
			return nil, errors.New("invalid API configuration file")
		}
		config, err := confserial.DecodeJSONConfig(io.LimitReader(f, 32<<20))
		if err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if args[1] == "ado" {
			if len(config.OutboundConfigs) != 1 {
				return nil, errors.New("one outbound required")
			}
			out, err := config.OutboundConfigs[0].Build()
			if err != nil {
				return nil, err
			}
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			_, err = handlers.AddOutbound(ctx, &handler.AddOutboundRequest{Outbound: out})
			return nil, err
		}
		if config.RouterConfig == nil {
			return nil, errors.New("routing configuration required")
		}
		rules, err := config.RouterConfig.Build()
		if err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		_, err = routing.AddRule(ctx, &router.AddRuleRequest{Config: serial.ToTypedMessage(rules), ShouldAppend: *appendRules})
		return nil, err
	case "statsquery":
		result, err := stats.NewStatsServiceClient(e.conn).QueryStats(ctx, &stats.QueryStatsRequest{Pattern: "sb-gateway>>>active>>>"})
		if err != nil {
			return nil, err
		}
		return json.Marshal(result.GetStat())
	case "statsonline":
		result, err := stats.NewStatsServiceClient(e.conn).GetStatsOnline(ctx, &stats.GetStatsRequest{Name: "user>>>" + *email + ">>>online"})
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"stat": map[string]any{"name": result.GetStat().GetName(), "value": result.GetStat().GetValue()}})
	default:
		return nil, fmt.Errorf("unsupported embedded API action %q", args[1])
	}
}

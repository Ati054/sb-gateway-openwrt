package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/agentport"
	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"github.com/Ati054/sb-gateway-openwrt/internal/health"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

var ErrRestartRequired = errors.New("configuration requires service restart")

type reloadRequest struct {
	operation string
	source    string
	signature string
	ctx       context.Context
	reply     chan reloadResult
}
type reloadResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Only health policy fields may change in place. Base routing, subscription,
// DNS and firewall changes retain the normal restart path.
// Capture-only edits replace the private nft tables atomically while Xray
// stays running; a service restart would temporarily remove interception.
func captureOnly(previous, next settings.Config) bool {
	previous.Capture = next.Capture
	return reflect.DeepEqual(previous, next)
}

func monitoringOnly(previous, next settings.Config) bool {
	strip := func(config settings.Config) settings.Config {
		config.Policies = append([]settings.Policy(nil), config.Policies...)
		for i := range config.Policies {
			config.Policies[i].Health = health.Policy{}
		}
		return config
	}
	return reflect.DeepEqual(strip(previous), strip(next))
}

func applyMonitoringReload(ctx context.Context, previous, next settings.Config, nodes []catalog.Node, poolPath string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !monitoringOnly(previous, next) {
		return ErrRestartRequired
	}
	if reflect.DeepEqual(previous, next) {
		return nil
	}
	return agentport.WritePool(poolPath, nodes, next.Policies)
}

func RequestReload(ctx context.Context, paths Paths) error {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", filepath.Join(paths.RunRoot, "reload.sock"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return ErrRestartRequired
		}
		return err
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	if err = json.NewEncoder(conn).Encode(struct {
		Operation string    `json:"operation"`
		Deadline  time.Time `json:"deadline"`
	}{"reload", deadline}); err != nil {
		return err
	}
	var result reloadResult
	if err = json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&result); err != nil {
		return err
	}
	switch result.Status {
	case "applied":
		return nil
	case "restart":
		return ErrRestartRequired
	default:
		return errors.New(result.Error)
	}
}

func startReloadServer(ctx context.Context, paths Paths, immediate ...func(reloadRequest) (reloadResult, bool)) (<-chan reloadRequest, func(), error) {
	path := filepath.Join(paths.RunRoot, "reload.sock")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, nil, err
	}
	requests := make(chan reloadRequest)
	stop := func() { listener.Close(); os.Remove(path) }
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				deadline, _ := requestCtx.Deadline()
				conn.SetDeadline(deadline.Add(time.Second))
				var message struct {
					Operation string    `json:"operation"`
					Source    string    `json:"source"`
					Signature string    `json:"signature"`
					Deadline  time.Time `json:"deadline"`
				}
				if err := json.NewDecoder(io.LimitReader(conn, 1024)).Decode(&message); err != nil || (message.Operation != "reload" && message.Operation != "refresh") {
					return
				}
				if !message.Deadline.IsZero() && message.Deadline.Before(deadline) {
					bounded, release := context.WithDeadline(requestCtx, message.Deadline)
					defer release()
					requestCtx = bounded
				}
				request := reloadRequest{operation: message.Operation, source: message.Source, signature: message.Signature, ctx: requestCtx, reply: make(chan reloadResult, 1)}
				result := reloadResult{Status: "error", Error: "reload busy or cancelled; retry Apply"}
				if len(immediate) > 0 {
					if answer, handled := immediate[0](request); handled {
						json.NewEncoder(conn).Encode(answer)
						return
					}
				}
				select {
				case requests <- request:
					select {
					case result = <-request.reply:
					case <-requestCtx.Done():
					}
				case <-requestCtx.Done():
				}
				json.NewEncoder(conn).Encode(result)
			}()
		}
	}()
	return requests, stop, nil
}

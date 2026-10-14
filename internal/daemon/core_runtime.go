package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
)

// Selected at build time for paired acceptance builds. It is deliberately not
// a user-facing UCI setting while the execution models are being compared.
var CoreExecutionMode = "process"

type runningCore struct {
	command     func(context.Context, string, ...string) ([]byte, error)
	close       func() error
	exited      <-chan error
	pid         int
	processName string
}

func (core *runningCore) Command(ctx context.Context, binary string, args ...string) ([]byte, error) {
	return core.command(ctx, binary, args...)
}
func (core *runningCore) Close() error { return core.close() }

func startCore(mode string, config []byte, configPath, binary, server string) (*runningCore, error) {
	switch mode {
	case "embedded":
		engine, err := xray.StartEmbedded(config, server)
		if err != nil {
			return nil, err
		}
		name, err := coreProcessName(os.Getpid())
		if err != nil {
			engine.Close()
			return nil, err
		}
		return &runningCore{command: engine.Command, close: engine.Close, pid: os.Getpid(), processName: name}, nil
	case "process":
		child := exec.Command(binary, "run", "-c", configPath)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		setCoreParentDeath(child)
		started, done := make(chan error, 1), make(chan error, 1)
		go func() {
			// Linux Pdeathsig tracks the creating thread, not just the process.
			// Keep that thread alive for the child's whole lifetime.
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			err := child.Start()
			started <- err
			if err == nil {
				done <- child.Wait()
			}
			close(done)
		}()
		if err := <-started; err != nil {
			return nil, err
		}
		stop := func() {
			select {
			case <-done:
				return
			default:
			}
			_ = child.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = child.Process.Kill()
				<-done
			}
		}
		client, err := xray.NewAPIClient(server)
		if err != nil {
			stop()
			return nil, err
		}
		name, err := coreProcessName(child.Process.Pid)
		if err != nil {
			client.Close()
			stop()
			return nil, err
		}
		return &runningCore{command: client.Command, close: func() error { err := client.Close(); stop(); return err }, exited: done, pid: child.Process.Pid, processName: name}, nil
	default:
		return nil, fmt.Errorf("unknown Xray execution mode %q", mode)
	}
}

func coreProcessName(pid int) (string, error) {
	body, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(body))
	if name == "" {
		return "", errors.New("empty Xray process name")
	}
	return name, nil
}

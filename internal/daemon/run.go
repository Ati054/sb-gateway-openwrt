package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/agentport"
	"github.com/Ati054/sb-gateway-openwrt/internal/capture"
	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"github.com/Ati054/sb-gateway-openwrt/internal/dnsproxy"
	"github.com/Ati054/sb-gateway-openwrt/internal/generation"
	"github.com/Ati054/sb-gateway-openwrt/internal/refresh"
	"github.com/Ati054/sb-gateway-openwrt/internal/resources"
	"github.com/Ati054/sb-gateway-openwrt/internal/rulesets"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
	"github.com/Ati054/sb-gateway-openwrt/internal/sip003"
	"github.com/Ati054/sb-gateway-openwrt/internal/subscription"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
)

type Paths struct {
	DataRoot string
	RunRoot  string
	Binary   string
}

func DefaultPaths() Paths {
	return Paths{DataRoot: "/etc/resproxy", RunRoot: "/var/run/resproxy", Binary: "/usr/libexec/resproxy/xray"}
}

func Run(ctx context.Context, config settings.Config, paths Paths) error {
	if !config.Enabled {
		return errors.New("service is disabled in UCI")
	}
	if config.RouteEnabled && !ProtectionActive(paths) {
		if err := ProtectCapture(ctx, config, paths); err != nil {
			return fmt.Errorf("protect LAN before startup: %w", err)
		}
	}
	if err := config.Base.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.RunRoot, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.DataRoot, 0o700); err != nil {
		return err
	}
	// Recover an interrupted takeover before constructing a new DNS path.
	if err := RestoreDNSMasqHandoff(paths); err != nil {
		return fmt.Errorf("restore router DNS: %w", err)
	}
	if err := RestoreDNSHandoff(paths); err != nil {
		return fmt.Errorf("restore https-dns-proxy: %w", err)
	}
	if err := rejectOtherXray(); err != nil {
		return err
	}
	rulesetSnapshot, compactedSnapshot, err := compactRulesetSnapshot(loadRulesetSnapshot(paths), filepath.Join(paths.DataRoot, "geoip-assets"))
	if err != nil {
		return fmt.Errorf("prepare shared domain assets: %w", err)
	}
	routingBase, err := withRulesets(config.Base, config.Policies, rulesetSnapshot, filepath.Join(paths.DataRoot, "geoip-assets"))
	if err != nil {
		return err
	}
	if err := retainGeoIPAssets(paths, routingBase); err != nil {
		return err
	}
	base, err := xray.RenderBase(routingBase)
	if err != nil {
		return err
	}
	basePath := filepath.Join(paths.RunRoot, "xray.json")
	if err := os.WriteFile(basePath, base, 0o600); err != nil {
		return err
	}
	passiveSocket := filepath.Join(paths.RunRoot, "passive-failures.sock")
	for key, value := range map[string]string{"RESPROXY_HEALTH_SOCKET": passiveSocket, "SB_XRAY_FAILURE_SOCKET": passiveSocket, "XRAY_LOCATION_ASSET": filepath.Join(paths.DataRoot, "geoip-assets")} {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	// Retained history is not proof that a newly started core serves its routes.
	if err := agentport.InvalidateRuntimeHealth(filepath.Join(paths.RunRoot, "health-state")); err != nil {
		return fmt.Errorf("invalidate previous runtime health: %w", err)
	}
	if err := os.Remove(filepath.Join(paths.RunRoot, "hot-ready.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	engine, err := startCore(CoreExecutionMode, base, basePath, paths.Binary, fmt.Sprintf("127.0.0.1:%d", config.Base.APIPort))
	if err != nil {
		return fmt.Errorf("start Xray (%s): %w", CoreExecutionMode, err)
	}
	supervisor := newSupervisedCore(engine)
	defer supervisor.Close()
	// Cancel slow refresh work immediately if a critical worker or core dies.
	// Workers finish before their core/API are closed.
	parentCtx := ctx
	ctx, cancelCause := context.WithCancelCause(ctx)
	cancelRun := func() { cancelCause(context.Canceled) }
	defer cancelRun()
	plugins := sip003.NewManager()
	defer plugins.Close()
	manager := xray.NewManager(paths.Binary, fmt.Sprintf("127.0.0.1:%d", config.Base.APIPort), paths.RunRoot)
	manager.LivenessSOCKS = fmt.Sprintf("127.0.0.1:%d", config.Base.SocksPort)
	manager.Run = supervisor.Command
	manager.PrepareOutbound = plugins.Prepare
	manager.ReleaseOutbound = plugins.Release
	if err := waitAPI(ctx, manager, "resproxy-"+config.Base.DefaultPolicy, engine.exited, xray.StartupTimeout(routingBase)); err != nil {
		return err
	}
	for _, policy := range config.Policies {
		if err := manager.Restore(ctx, "resproxy-"+policy.ID, "block"); err != nil {
			return fmt.Errorf("initialize fail-closed policy %q: %w", policy.ID, err)
		}
	}
	if err := manager.Restore(ctx, xray.RefreshProbeTag, "block"); err != nil {
		return fmt.Errorf("initialize isolated subscription probe: %w", err)
	}
	readyPath := filepath.Join(paths.RunRoot, "xray-ready")
	if err := os.WriteFile(readyPath, []byte(strconv.Itoa(engine.pid)+"\n"), 0o600); err != nil {
		return err
	}
	defer os.Remove(readyPath)
	store := generation.Store{Root: paths.DataRoot, MetadataRoot: paths.RunRoot}
	poolPath := filepath.Join(paths.RunRoot, "health-pool.json")
	_, nodes, err := store.Current()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("current subscription generation is invalid: %w", err)
	}
	nodes, err = retainConfiguredSources(store, nodes, config.Subscriptions)
	if err != nil {
		return fmt.Errorf("remove unconfigured subscription sources: %w", err)
	}
	publishCatalogSummary(paths.RunRoot, nodes)
	if err := agentport.WritePool(poolPath, nodes, config.Policies); err != nil {
		return err
	}
	if err := store.Prune(3); err != nil {
		log.Printf("resproxy: subscription storage pruning skipped: %v", err)
	}
	var updateDNS func([]dnsproxy.Policy) error
	recoveringCapture := CrashFallbackActive(paths) || ProtectionActive(paths)
	captureDNSPort := 0
	dnsInterop := DNSInterop{RequestedMode: config.DNSMode, EffectiveMode: "off"}
	if config.RouteEnabled {
		handedOff := false
		directBootstrapIP := ""
		if config.DNSMode != "system" {
			if config.DNSHandoffConfirmed && httpsDNSProxyOps().running() {
				directBootstrapIP, err = dnsproxy.BootstrapDirect(ctx, config.DNSDirectURL)
				if err != nil {
					log.Printf("resproxy: direct WAN DNS preflight failed; https-dns-proxy retained: %v", err)
				} else {
					handedOff, err = acquireDNSHandoff(dnsHandoffFile(paths), true, httpsDNSProxyOps())
					if err != nil {
						log.Printf("resproxy: https-dns-proxy handoff failed; existing DNS retained: %v", err)
					}
				}
			}
		}
		if handedOff {
			defer func() {
				if err := RestoreManagedDNS(paths); err != nil {
					log.Printf("resproxy: restore managed DNS after exit: %v", err)
				}
			}()
		}

		dnsInterop = resolveDNSInterop(ctx, config.DNSMode, currentNFTRules, activeExternalDNSService)
		if handedOff && dnsInterop.Reason == "other-dns-redirect" {
			// The other service's firewall reload can lag behind its stop.
		handoffFirewallWait:
			for attempts := 0; attempts < 10 && dnsInterop.Reason == "other-dns-redirect"; attempts++ {
				select {
				case <-ctx.Done():
					break handoffFirewallWait
				case <-time.After(500 * time.Millisecond):
				}
				dnsInterop = resolveDNSInterop(ctx, config.DNSMode, currentNFTRules, activeExternalDNSService)
			}
		}
		if handedOff && dnsInterop.EffectiveMode == "system" {
			if err := RestoreDNSHandoff(paths); err != nil {
				log.Printf("resproxy: restore https-dns-proxy after DNS conflict: %v", err)
			}
		}
		dnsPort := 0
		if dnsInterop.EffectiveMode != "system" {
			policyProxyPorts := make(map[string]int, len(routingBase.Policies))
			for index, policy := range routingBase.Policies {
				policyProxyPorts[policy.ID] = routingBase.DNSPolicyBasePort + index
			}
			stopDNS, update, err := dnsproxy.Start(ctx, dnsproxy.Config{
				Port: config.DNSPort, ProxyPort: config.Base.HTTPPort,
				PolicyProxyPorts: policyProxyPorts,
				ProxyURL:         config.DNSProxyURL, DirectURL: config.DNSDirectURL,
				DirectBootstrapIP: directBootstrapIP,
				Mode:              dnsInterop.EffectiveMode, DefaultPolicy: config.Base.DefaultPolicy,
				Policies:        dnsPoliciesFromBase(routingBase),
				HealthStatePath: filepath.Join(paths.RunRoot, "health-state", "selector-runtime.json"),
				StatusPath:      filepath.Join(paths.RunRoot, "dns-status.json"),
				GenerationPath:  filepath.Join(paths.RunRoot, "dns-generation.json"),
			})
			if err != nil {
				dnsInterop.EffectiveMode = "system"
				dnsInterop.Reason = "listener-failed"
				log.Printf("resproxy: LAN DNS listener unavailable; existing DNS preserved: %v", err)
				if err := RestoreDNSHandoff(paths); err != nil {
					log.Printf("resproxy: restore https-dns-proxy after DNS failure: %v", err)
				}
			} else {
				defer stopDNS()
				updateDNS = update
				dnsPort = config.DNSPort
				if handedOff {
					ops := systemDNSMasqOps()
					ops.LANInterface = config.LANInterface
					err = acquireDNSMasqHandoff(dnsmasqHandoffFile(paths), dnsPort, ops)
					if err == nil {
						err = verifyRouterDNS(ctx)
					}
					if err != nil {
						log.Printf("resproxy: router DNS takeover failed; restoring previous DNS: %v", err)
						if restoreErr := RestoreDNSMasqHandoff(paths); restoreErr != nil {
							return fmt.Errorf("restore router DNS after failed takeover: %w", restoreErr)
						}
						if restoreErr := RestoreDNSHandoff(paths); restoreErr != nil {
							return fmt.Errorf("restore https-dns-proxy after failed takeover: %w", restoreErr)
						}
						dnsInterop.EffectiveMode = "system"
						dnsInterop.Reason = "router-dns-handoff-failed"
						dnsPort = 0
					}
				}
			}
		}
		if dnsInterop.Reason != "" {
			log.Printf("resproxy: DNS coexistence: %s", dnsInterop.Reason)
		}
		if err := publishDNSInterop(paths, dnsInterop); err != nil {
			log.Printf("resproxy: DNS coexistence status unavailable: %v", err)
		}
		captureDNSPort = dnsPort
		if !recoveringCapture {
			if err := waitInitialCapture(ctx, func(attempt context.Context) error {
				return setFirewall(attempt, "apply", config.LANInterface, config.Base.TransparentPort, dnsPort, config.Capture)
			}, 2*time.Second); err != nil {
				return fmt.Errorf("enable LAN capture: %w", err)
			}
		}
		// The init lifecycle owns removal after the daemon has fully exited.
		// A late startup/cancellation must never remove a newer restart guard.
		defer log.Printf("resproxy: retaining LAN policy until init completes the transition")
	}
	if !config.RouteEnabled {
		if err := publishDNSInterop(paths, dnsInterop); err != nil {
			log.Printf("resproxy: DNS coexistence status unavailable: %v", err)
		}
	}
	defer os.Remove(dnsInteropPath(paths))
	control := newSubscriptionControl(config)
	control.applyReload = func(requestCtx context.Context, previous, next settings.Config) error {
		if captureOnly(previous, next) && !reflect.DeepEqual(previous.Capture, next.Capture) {
			return RestoreFirewall(requestCtx, next, paths)
		}
		return agentport.UpdatePoolHealth(requestCtx, poolPath, next.Policies)
	}
	reloadRequests, stopReload, err := startReloadServer(ctx, paths, control.handle)
	if err != nil {
		return fmt.Errorf("start configuration reload: %w", err)
	}
	defer stopReload()
	progress := newHealthProgress()
	supervisor.mu.Lock()
	supervisor.onOOM = func() {
		if body, err := json.Marshal(time.Now().UTC()); err == nil {
			_ = os.WriteFile(filepath.Join(paths.RunRoot, "last-oom.json"), body, 0600)
		}
	}
	supervisor.mu.Unlock()
	healthOptions := agentport.Options{
		OnProgress:      progress.mark,
		BackgroundBusy:  resources.NewBackgroundBusy(),
		XrayProcessName: engine.processName, RunXrayCommand: supervisor.Command,
		PrepareOutbound: plugins.Prepare, ReleaseOutbound: plugins.Release,
		StateRoot:  filepath.Join(paths.RunRoot, "health-state"),
		XrayBinary: paths.Binary, XrayAPIServer: manager.Server,
		XrayReadyFile: readyPath, HealthInterval: 60 * time.Second,
		HealthPoolFile: poolPath, DNSGenerationFile: filepath.Join(paths.RunRoot, "dns-generation.json"), HotRuntimeReadyFile: filepath.Join(paths.RunRoot, "hot-ready.json"),
		PassiveSocket: passiveSocket,
		ProbeURL:      fmt.Sprintf("http://127.0.0.1:%d", config.Base.ProbeBasePort),
		ProbeBasePort: config.Base.ProbeBasePort,
	}
	healthDone := make(chan struct{})
	defer func() { cancelRun(); <-healthDone }()
	go func() {
		defer close(healthDone)
		superviseCritical(ctx, cancelCause, "health controller", func(ctx context.Context) error {
			return agentport.RunHealth(ctx, healthOptions)
		})
	}()
	updatesReady := make(chan struct{})
	var background sync.WaitGroup
	background.Add(3)
	go func() { defer background.Done(); monitorResources(ctx, paths, supervisor) }()
	defer func() { cancelRun(); background.Wait() }()
	go func() { defer background.Done(); runGeoIPCleanup(ctx, paths) }()
	// Copy the initial configuration before scheduling the worker: the main
	// refresh loop replaces its own config variable with live snapshots.
	go func(initial settings.Config) {
		defer background.Done()
		select {
		case <-ctx.Done():
			return
		case <-updatesReady:
		}

		runRulesetUpdates(ctx, initial, paths, manager, rulesetSnapshot, routingBase, updateDNS)
	}(config)
	background.Add(2)
	go func() {
		defer background.Done()
		err := supervisor.run(ctx, xray.StartupTimeout(routingBase)+15*time.Second,
			func(check context.Context) error {
				return manager.CheckLiveness(check)
			},
			func() error {
				err := os.Remove(readyPath)
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			},
			func(recovery context.Context) (*runningCore, error) {
				coreLifecycleMu.Lock()
				defer coreLifecycleMu.Unlock()
				current := control.snapshot()
				recoveredBase, err := withRulesets(current.Base, current.Policies, loadRulesetSnapshot(paths), filepath.Join(paths.DataRoot, "geoip-assets"))
				if err != nil {
					return nil, err
				}
				body, err := xray.RenderBase(recoveredBase)
				if err != nil {
					return nil, err
				}
				if err = os.WriteFile(basePath, body, 0600); err != nil {
					return nil, err
				}
				replacement, err := startCore(CoreExecutionMode, body, basePath, paths.Binary, manager.Server)
				if err != nil {
					return nil, err
				}
				success := false
				defer func() {
					if !success {
						replacement.Close()
					}
				}()
				local := xray.NewManager(paths.Binary, manager.Server, paths.RunRoot)
				local.Run = replacement.Command
				if err = waitAPI(recovery, local, "resproxy-"+current.Base.DefaultPolicy, replacement.exited, xray.StartupTimeout(recoveredBase)); err != nil {
					return nil, err
				}
				selectors := make([]string, 0, len(current.Policies))
				for _, policy := range current.Policies {
					selectors = append(selectors, "resproxy-"+policy.ID)
				}
				if err = initializeCoreSelectors(recovery, local, selectors); err != nil {
					return nil, err
				}
				if err = recovery.Err(); err != nil {
					return nil, err
				}
				if err = os.WriteFile(readyPath, []byte(strconv.Itoa(replacement.pid)+"\n"), 0600); err != nil {
					return nil, err
				}
				success = true
				return replacement, nil
			})
		if ctx.Err() == nil {
			cancelCause(err)
		}
	}()
	go func() {
		defer background.Done()
		instance := os.Getenv("RESPROXY_PROCD_INSTANCE")
		watchProcd(ctx, instance, progress, func(ctx context.Context) error {
			if supervisor.Recovering(time.Now()) {
				return nil
			}
			return manager.CheckLiveness(ctx)
		}, func(ctx context.Context) error { return renewProcdWatchdog(ctx, instance) })
	}()
	// Recovery must run while initial routes are being confirmed. The supervisor
	// alone consumes child exits; startup waits on its fatal cancellation.
	if err := waitStartupRoutes(ctx, config, paths, nil, healthDone); err != nil {
		return err
	}
	if config.RouteEnabled && recoveringCapture {
		if err := waitInitialCapture(ctx, func(attempt context.Context) error {
			return setFirewall(attempt, "apply", config.LANInterface, config.Base.TransparentPort, captureDNSPort, config.Capture)
		}, 2*time.Second); err != nil {
			return fmt.Errorf("restore recovered LAN capture: %w", err)
		}
		if err := os.Remove(crashFallbackFile(paths)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := ClearProtection(paths); err != nil {
		return err
	}
	if err := markCrashReadyNow(paths); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(paths.RunRoot, "daemon.pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(paths.RunRoot, "daemon.pid"))
	if compactedSnapshot {
		if err := rulesets.Save(rulesetSnapshotPath(paths), rulesetSnapshot); err != nil {
			log.Printf("resproxy: domain cache migration not persisted: %v", err)
		}
	}

	close(updatesReady)
	service := &refresh.Service{Store: store, Runtime: manager,
		AfterSwitch: func(previous, next []catalog.Node) (func() error, error) {
			if err := control.publishPool(poolPath, next); err != nil {
				return nil, err
			}
			return func() error { return control.publishPool(poolPath, previous) }, nil
		},
	}
	sourceUpdates := newSubscriptionUpdates(paths, config.Subscriptions)
	retirement := &retirementQueue{}
	nextRefresh := make(map[string]time.Time, len(config.Subscriptions))
	etags := make(map[string]string, len(config.Subscriptions))
	probeCursors := make(map[string]int, len(config.Subscriptions))
	requestedID := ""
	for _, source := range config.Subscriptions {
		nextRefresh[source.ID] = time.Now()
	}
	if len(nodes) > 0 {
		// Give health startup a chance to restore the prior live selection.
		for id := range nextRefresh {
			nextRefresh[id] = nextRefresh[id].Add(10 * time.Second)
		}
	}
	var clock routerClock
	daily := dailyRefreshSchedule{}
	hasDaily := false
	for _, source := range config.Subscriptions {
		hasDaily = hasDaily || source.UpdateMode == "daily"
	}
	clockFailed := false
	background.Add(2)
	go func() { defer background.Done(); watchPlugins(ctx, plugins, manager) }()
	go func() {
		defer background.Done()
		watchVPNRoutes(ctx, func(ctx context.Context) error {
			current, err := settings.ReadSystemUCI(ctx)
			if err != nil {
				return err
			}
			return RestoreFirewall(ctx, current, paths)
		})
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		config = control.snapshot()
		var wallNow time.Time
		if hasDaily {
			var clockErr error
			wallNow, clockErr = clock.now(ctx, time.Now())
			if clockErr != nil && !clockFailed {
				log.Printf("resproxy: scheduled subscription updates paused: %v", clockErr)
			}
			clockFailed = clockErr != nil
		}
		for _, source := range config.Subscriptions {
			requested := requestedID == "*" || requestedID == source.ID
			due := sourceRefreshDue(source, time.Now(), nextRefresh[source.ID], requested)
			if source.UpdateMode == "daily" {
				due = requested || (!wallNow.IsZero() && daily.due(source, wallNow) && !time.Now().Before(nextRefresh[source.ID]))
			}
			if !due {
				continue
			}
			if err := retirement.Sweep(ctx, time.Now(), "resproxy-"+config.Base.DefaultPolicy, manager); err != nil {
				log.Printf("resproxy: postponed old outbound retirement: %v", err)
			}
			if !retirement.HasCapacity() {
				log.Printf("resproxy: subscription %s refresh postponed: Xray outbound cleanup backlog is full", source.ID)
				nextRefresh[source.ID] = time.Now().Add(30 * time.Second)
				continue
			}
			if source.UpdateMode == "daily" && !wallNow.IsZero() {
				daily.attempted(source, wallNow)
			}
			_, before, _ := store.Current()
			attempt := sourceUpdates.begin(source.ID, before)
			cursor := probeCursors[source.ID]
			newETag, oldTag, err := refreshOnce(ctx, source, config, paths, service, manager, etags[source.ID], &cursor)
			_, after, _ := store.Current()
			sourceUpdates.finish(source.ID, attempt, before, after, err)
			probeCursors[source.ID] = cursor
			if pruneErr := store.Prune(3); pruneErr != nil {
				log.Printf("resproxy: subscription storage pruning skipped: %v", pruneErr)
			}
			if err != nil {
				log.Printf("resproxy: subscription %s refresh preserved previous generation: %v", source.ID, err)
				nextRefresh[source.ID] = time.Now().Add(30 * time.Second)
			} else {
				etags[source.ID] = newETag
				retirement.Observe(oldTag, time.Now())
				if _, current, currentErr := store.Current(); currentErr == nil {
					log.Printf("resproxy: subscription %s refresh completed; active catalog has %d nodes", source.ID, len(current))
				}
				nextRefresh[source.ID] = time.Now().Add(time.Duration(source.UpdateSeconds) * time.Second)
			}
		}
		requestedID = ""
		select {
		case <-ctx.Done():
			if parentCtx.Err() != nil {
				return nil
			}
			return context.Cause(ctx)
		case source := <-control.refresh:
			requestedID = source
		case request := <-reloadRequests:
			if request.operation == "refresh" {
				result := refreshAcceptance(config.Subscriptions, request.source, request.signature)
				if request.ctx.Err() != nil {
					result = reloadResult{Status: "error", Error: "refresh request expired"}
				} else if result.Status == "applied" {
					requestedID = request.source
				}
				request.reply <- result
				continue
			}
			result := reloadResult{Status: "error"}
			candidate, reloadErr := settings.ReadSystemUCI(request.ctx)
			if reloadErr == nil {
				_, currentNodes, loadErr := store.Current()
				if errors.Is(loadErr, os.ErrNotExist) {
					loadErr = nil
				}
				reloadErr = loadErr
				if reloadErr == nil {
					if captureOnly(config, candidate) && !reflect.DeepEqual(config.Capture, candidate.Capture) {
						reloadErr = RestoreFirewall(request.ctx, candidate, paths)
					} else {
						reloadErr = applyMonitoringReload(request.ctx, config, candidate, currentNodes, poolPath)
					}
				}
			}
			if reloadErr == nil {
				config = candidate
				control.update(candidate)
				result.Status = "applied"
				log.Printf("resproxy: configuration applied without restarting Xray")
			} else if errors.Is(reloadErr, ErrRestartRequired) {
				result.Status = "restart"
			} else {
				result.Error = reloadErr.Error()
			}
			request.reply <- result
		case <-ticker.C:
			if err := retirement.Sweep(ctx, time.Now(), "resproxy-"+config.Base.DefaultPolicy, manager); err != nil {
				log.Printf("resproxy: postponed old outbound retirement: %v", err)
			}

		}
	}
}

func verifyRouterDNS(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:53")
	}}
	addresses, err := resolver.LookupNetIP(checkCtx, "ip4", "example.com")
	if err != nil || len(addresses) == 0 {
		return fmt.Errorf("router DNS did not answer through managed WAN path: %w", err)
	}
	return nil
}

func retainConfiguredSources(store generation.Store, nodes []catalog.Node, sources []settings.Subscription) ([]catalog.Node, error) {
	if len(nodes) == 0 {
		return nodes, nil
	}
	allowed := make(map[string]bool, len(sources))
	for _, source := range sources {
		allowed[source.ID] = true
	}
	retained := make([]catalog.Node, 0, len(nodes))
	for _, node := range nodes {
		if allowed[node.ProviderID] {
			retained = append(retained, node)
		}
	}
	if len(retained) == len(nodes) {
		return nodes, nil
	}
	if len(retained) == 0 {
		if err := store.ClearCurrent(); err != nil {
			return nil, err
		}
	} else {
		id, err := store.Stage(retained)
		if err != nil {
			return nil, err
		}
		if err := store.Publish(id); err != nil {
			return nil, err
		}
	}
	log.Printf("resproxy: removed %d nodes from unconfigured subscription sources", len(nodes)-len(retained))
	return retained, nil
}

func refreshDue(config settings.Config, now, next time.Time, requested bool) bool {
	return sourceRefreshDue(settings.Subscription{URL: config.SubscriptionURL, UpdateMode: config.UpdateMode}, now, next, requested)
}

func sourceRefreshDue(source settings.Subscription, now, next time.Time, requested bool) bool {
	return source.URL != "" && (requested || (source.UpdateMode == "automatic" && !now.Before(next)))
}

func setFirewall(ctx context.Context, action, iface string, port, dnsPort int, policy capture.Policy) error {
	arguments := []string{action}
	if action == "apply" || action == "emergency" || action == "guard" {
		arguments = append(arguments, iface, strconv.Itoa(port), strconv.Itoa(dnsPort), policy.IPv6Mode)
		if policy.BlockPing {
			arguments = append(arguments, "--block-ping")
		}
		arguments = append(arguments, policy.DirectIPs...)
	}
	output, err := boundedCommand(ctx, "/usr/libexec/resproxy-firewall", arguments...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func dnsPoliciesFromBase(base xray.BaseConfig) []dnsproxy.Policy {
	policies := make([]dnsproxy.Policy, 0, len(base.Policies))
	for _, policy := range base.Policies {
		rules := make([]dnsproxy.Rule, 0, len(policy.Rules))
		for _, rule := range policy.Rules {
			// DNS queries do not carry application ports/protocols or inbound tags.
			// Keep the policy resolver for conditional traffic rules.
			if rule.Network != "" && rule.Network != "tcp,udp" || len(rule.Protocols)+len(rule.Inbounds) > 0 || rule.Ports != "" {
				continue
			}
			rules = append(rules, dnsproxy.Rule{Action: rule.Action, OutageAction: rule.OutageAction, Domains: append(append(append([]string(nil), rule.Domains...), rule.CountryDomains...), rule.ServiceDomains...)})
		}
		behavior, _ := policy.ProxyOutageBehavior()
		policies = append(policies, dnsproxy.Policy{
			ProxyOutageAction: behavior, ID: policy.ID, SourceCIDRs: policy.SourceCIDRs,
			Rules: rules,
		})
	}
	return policies
}

func refreshOnce(ctx context.Context, source settings.Subscription, config settings.Config, paths Paths, service *refresh.Service, manager *xray.Manager, previousETag string, probeCursor *int) (string, string, error) {
	release, leaseErr := resources.BeginUpdate(ctx)
	if leaseErr != nil {
		return "", "", leaseErr
	}
	defer release()
	_, existing, currentErr := service.Store.Current()
	proxyAvailable := currentErr == nil && len(existing) > 0
	ports := fetchPorts(source.FetchVia, proxyAvailable, config.Base.HTTPPort)
	if len(ports) == 0 {
		return "", "", errors.New("proxy-only fetch requested before a live catalog exists")
	}
	var body []byte
	var etag string
	var unchanged bool
	var err error
	for _, port := range ports {
		client := subscription.NewHTTPClient(port)
		body, etag, unchanged, err = subscription.Fetch(ctx, client, source.URL, previousETag)
		client.CloseIdleConnections()
		if err == nil {
			break
		}
	}
	if err != nil || unchanged {
		return etag, "", err
	}
	chosen := func(nodes []catalog.Node) (string, error) {
		return chooseAfterProbe(ctx, nodes, existing, config, paths, manager, probeCursor)
	}
	result, err := service.Apply(ctx, source.ID, body, source.MaxNodes, "resproxy-"+config.Base.DefaultPolicy, chosen)
	if err != nil {
		return "", "", err
	}
	if stats := result.ParseStats; stats.Skipped() > 0 {
		log.Printf("resproxy: subscription %q imported with skipped entries: invalid=%d unsupported=%d limit=%d", source.ID, stats.Invalid, stats.Unsupported, stats.Limited)
	}
	return etag, result.OldTag, nil
}

func fetchPorts(mode string, available bool, proxyPort int) []int {
	switch mode {
	case "direct-only":
		return []int{0}
	case "proxy-only":
		if available {
			return []int{proxyPort}
		}
		return nil
	case "direct-first":
		if available {
			return []int{0, proxyPort}
		}
		return []int{0}
	default:
		if available {
			return []int{proxyPort, 0}
		}
		return []int{0}
	}
}

func activeNode(path, selector string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// The health state is trusted local tmpfs data. A small partial decode keeps
	// subscription refresh independent of monitoring details.
	var states map[string]struct {
		Selected string `json:"selected"`
	}
	if json.Unmarshal(body, &states) != nil {
		return ""
	}
	return states[selector].Selected
}

func waitAPI(ctx context.Context, manager *xray.Manager, selector string, exited <-chan error, budget time.Duration) error {
	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		_, err := manager.Current(probeCtx, selector)
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("Xray API did not become ready")
		case err := <-exited:
			return fmt.Errorf("Xray exited during startup: %w", err)
		case <-ticker.C:
		}
	}
}

func rejectOtherXray() error {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil // Non-Linux unit-test host.
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		body, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
		if err == nil && strings.TrimSpace(string(body)) == "xray" {
			return errors.New("another Xray process is already running; disable its separate service before enabling resproxy")
		}
	}
	return nil
}

// RestoreFirewall preserves the effective DNS handoff when netifd removes
// TProxy plumbing. A missing handoff record must not enable interception.
func RestoreFirewall(ctx context.Context, config settings.Config, paths Paths) error {
	if !config.Enabled || !config.RouteEnabled {
		return nil
	}
	if ProtectionActive(paths) {
		return setFirewall(ctx, "guard", config.LANInterface, config.Base.TransparentPort, 0, config.Capture)
	}
	if CrashFallbackActive(paths) {
		return setFirewall(ctx, "emergency", config.LANInterface, config.Base.TransparentPort, 0, config.Capture)
	}
	body, err := os.ReadFile(dnsInteropPath(paths))
	if err != nil {
		return err
	}
	var state DNSInterop
	if err = json.Unmarshal(body, &state); err != nil {
		return err
	}
	dnsPort := 0
	switch state.EffectiveMode {
	case "system":
	case "proxy-only", "proxy-fallback-wan":
		dnsPort = config.DNSPort
	default:
		return errors.New("invalid effective DNS mode")
	}
	return setFirewall(ctx, "apply", config.LANInterface, config.Base.TransparentPort, dnsPort, config.Capture)
}

func watchVPNRoutes(ctx context.Context, restore func(context.Context) error) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	fingerprint := func() string {
		inventory := capture.Discover(ctx)
		if !inventory.Complete {
			return ""
		}
		// Prefix changes are already followed by FIB. Only new interfaces and outer endpoints need new nft rules.
		endpoints := []capture.AutomaticIP{}
		for _, v := range inventory.Addresses {
			if v.Kind == "endpoint" {
				endpoints = append(endpoints, v)
			}
		}
		body, _ := json.Marshal(struct {
			Interfaces []string
			Endpoints  []capture.AutomaticIP
		}{inventory.Interfaces, endpoints})
		return string(body)
	}
	previous := fingerprint()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			next := fingerprint()
			if next == "" || next == previous {
				continue
			}
			refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := restore(refreshCtx)
			cancel()
			if err == nil {
				previous = next
			} else {
				log.Printf("resproxy: VPN capture exclusions refresh failed: %v", err)
			}
		}
	}
}

func watchPlugins(ctx context.Context, plugins *sip003.Manager, manager *xray.Manager) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if !plugins.HasEntries() {
				continue
			}
			tags, err := manager.Inventory(ctx)
			if err != nil {
				tags = nil
			}
			plugins.Sweep(ctx, tags)
		}
	}
}

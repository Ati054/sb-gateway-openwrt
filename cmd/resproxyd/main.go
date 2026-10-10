package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"example.org/resilient-proxy/internal/agentport"
	"example.org/resilient-proxy/internal/capture"
	"example.org/resilient-proxy/internal/daemon"
	"example.org/resilient-proxy/internal/generation"
	"example.org/resilient-proxy/internal/releaseupdate"
	"example.org/resilient-proxy/internal/rulesets"
	"example.org/resilient-proxy/internal/selection"
	"example.org/resilient-proxy/internal/settings"
	"example.org/resilient-proxy/internal/sip003"
	"example.org/resilient-proxy/internal/xray"
	"example.org/resilient-proxy/internal/xraycli"
)

var version = "0.1.0"

func main() {
	if filepath.Base(os.Args[0]) == "xray" {
		xraycli.Main()
		return
	}
	if err := execute(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "resproxyd:", err)
		if errors.Is(err, daemon.ErrRestartRequired) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func execute(arguments []string) error {
	if len(arguments) > 0 && arguments[0] == "capture-rules" {
		if len(arguments) < 5 || len(arguments) > 261 {
			return errors.New("usage: capture-rules IFNAME PORT DNSPORT IPV6MODE [IP...]")
		}
		port, err := strconv.Atoi(arguments[2])
		if err != nil {
			return err
		}
		dnsPort, err := strconv.Atoi(arguments[3])
		if err != nil {
			return err
		}
		policy, err := capture.New(arguments[4], arguments[5:])
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		policy.Automatic = capture.Discover(ctx)
		cancel()
		if !policy.Automatic.Complete {
			return errors.New("VPN interface discovery unavailable; previous capture rules retained")
		}
		body, err := capture.Render(arguments[1], port, dnsPort, policy)
		if err != nil {
			return err
		}
		_, err = fmt.Print(body)
		return err
	}
	if len(arguments) < 1 || len(arguments) > 2 {
		return errors.New("usage: resproxyd {run|reload|render-base|refresh [source-id]|status|health|locations|capture-restore|rulesets|rulesets-refresh|dns-restore|version}")
	}
	if len(arguments) > 1 && arguments[0] != "refresh" && arguments[0] != "plugins-install" && arguments[0] != "plugins-worker" {
		return errors.New("only refresh accepts a source ID")
	}
	paths := daemon.DefaultPaths()
	switch arguments[0] {
	case "update-check", "update-download":
		timeout := 90 * time.Second
		if arguments[0] == "update-download" {
			timeout = 8 * time.Minute
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if arguments[0] == "update-check" {
			return releaseupdate.CheckWorker(ctx)
		}
		return releaseupdate.DownloadWorker(ctx)

	case "plugins-status":
		return json.NewEncoder(os.Stdout).Encode(sip003.Status())
	case "plugins-install", "plugins-worker":
		if len(arguments) != 2 {
			return errors.New("plugin name is required")
		}
		if arguments[0] == "plugins-worker" {
			return sip003.InstallWorker(arguments[1])
		}
		return sip003.BeginInstall(arguments[1])
	case "reload":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return daemon.RequestReload(ctx, paths)
	case "version":
		fmt.Println(version)
		return nil
	case "dns-restore":
		if err := daemon.RestoreDNSMasqHandoff(paths); err != nil {
			return err
		}
		return daemon.RestoreDNSHandoff(paths)
	case "refresh":
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		config, err := settings.ReadSystemUCI(ctx)
		if err != nil {
			return err
		}
		if !config.Enabled {
			return errors.New("service is disabled")
		}
		requested := "*"
		if len(arguments) == 2 {
			requested = arguments[1]
		}
		return daemon.RequestRefresh(ctx, paths, config.Subscriptions, requested)
	case "status":
		return printStatus(paths)
	case "rulesets":
		choices, err := rulesets.Featured()
		if err != nil {
			return err
		}
		var status any
		if body, err := os.ReadFile(filepath.Join(paths.RunRoot, "rulesets-status.json")); err == nil {
			_ = json.Unmarshal(body, &status)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		inventory := capture.Discover(ctx)
		var automatic any
		if inventory.Complete {
			automatic = inventory.Addresses
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"choices": choices, "status": status, "automatic_ips": automatic})
	case "rulesets-refresh":
		if !running(paths) {
			return errors.New("service is not running")
		}
		return os.WriteFile(filepath.Join(paths.RunRoot, "rulesets-refresh.request"),
			[]byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600)
	case "health":
		return printHealth(paths)
	case "capture-restore":
		if !running(paths) {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		config, err := settings.ReadSystemUCI(ctx)
		if err != nil {
			return err
		}
		return daemon.RestoreFirewall(ctx, config, paths)
	case "locations":
		return printLocations(paths)
	case "run", "render-base":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		config, err := settings.ReadSystemUCI(ctx)
		cancel()
		if err != nil {
			return err
		}
		if arguments[0] == "render-base" {
			body, err := xray.RenderBase(config.Base)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(append(body, '\n'))
			return err
		}
		runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return daemon.Run(runCtx, config, paths)
	default:
		return errors.New("unknown subcommand")
	}
}

func printLocations(paths daemon.Paths) error {
	_, nodes, err := (generation.Store{Root: paths.DataRoot}).Current()
	if errors.Is(err, os.ErrNotExist) {
		nodes = nil
	} else if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(selection.PublicInventory(nodes))
}

func printStatus(paths daemon.Paths) error {
	_, nodes, err := (generation.Store{Root: paths.DataRoot}).Current()
	if errors.Is(err, os.ErrNotExist) {
		nodes = nil
	} else if err != nil {
		return err
	}
	type policyState struct {
		Selected         string `json:"selected"`
		RuntimeSelected  string `json:"runtime_selected"`
		RuntimeConfirmed bool   `json:"runtime_confirmed"`
		RuntimeError     string `json:"runtime_error"`
		LastSwitchReason string `json:"last_switch_reason"`
		CheckedAt        string `json:"checked_at"`
	}
	states := map[string]policyState{}
	if body, err := os.ReadFile(filepath.Join(paths.RunRoot, "health-state", "selector-health.json")); err == nil {
		_ = json.Unmarshal(body, &states)
	}
	var dnsStatus any
	if body, err := os.ReadFile(filepath.Join(paths.RunRoot, "dns-status.json")); err == nil {
		_ = json.Unmarshal(body, &dnsStatus)
	}
	var dnsInterop any
	if body, err := os.ReadFile(filepath.Join(paths.RunRoot, "dns-interop.json")); err == nil {
		_ = json.Unmarshal(body, &dnsInterop)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"running": running(paths), "nodes": len(nodes), "policies": states,
		"dns": dnsStatus, "dns_interop": dnsInterop,
		"subscriptions":        daemon.SubscriptionUpdates(paths, nodes),
		"external_dns_service": daemon.ActiveExternalDNSService(),
	})
}

type nodeHealth struct {
	LatestDelayMS   *int                       `json:"latest_delay_ms"`
	LatestQualityAt float64                    `json:"latest_quality_at,omitempty"`
	LatencyDelta    *float64                   `json:"latency_delta_percent"`
	DeltaReason     string                     `json:"latency_delta_reason,omitempty"`
	CurrentMedianMS *int                       `json:"current_median_ms"`
	LastGoodAt      float64                    `json:"last_good_at,omitempty"`
	ID              string                     `json:"id"`
	Label           string                     `json:"label"`
	Order           *int                       `json:"candidate_order,omitempty"`
	Selected        bool                       `json:"selected"`
	Available       bool                       `json:"available"`
	LastProbeAt     float64                    `json:"last_probe_at,omitempty"`
	FailureClass    string                     `json:"failure_class,omitempty"`
	PeriodStats     map[string]nodeHealthStats `json:"period_stats"`
}

type nodeHealthStats struct {
	Samples      int      `json:"samples"`
	Availability *float64 `json:"availability_percent,omitempty"`
	MedianMS     *int     `json:"median_ms,omitempty"`
	Loss         *float64 `json:"https_error_percent,omitempty"`
}

type policyHealth struct {
	HistoryTruncated bool         `json:"history_truncated,omitempty"`
	HistoryBudgetKiB int          `json:"history_budget_kib,omitempty"`
	HistoryUsageKiB  int          `json:"history_usage_kib,omitempty"`
	ID               string       `json:"id"`
	Mode             string       `json:"mode"`
	Selected         string       `json:"selected"`
	HealthyReserves  int          `json:"healthy_reserves"`
	CandidateCount   int          `json:"candidate_count"`
	CheckedAt        string       `json:"checked_at"`
	UnderlayFailure  string       `json:"underlay_failure,omitempty"`
	Nodes            []nodeHealth `json:"nodes"`
}

// UI comparisons describe the last ordinary successful HTTPS measurements. They
// do not require recovery confirmations and never qualify a node for switching.
// Recalculate against the confirmed active node, including just after a switch.
func setFreshLatencyDeltas(nodes []nodeHealth, now time.Time, activeSeconds, backupSeconds int) {
	if activeSeconds <= 0 {
		activeSeconds = 60
	}
	window := max(activeSeconds*2, 120)
	if backupSeconds > 0 {
		window = min(window, backupSeconds)
	}
	fresh := func(n nodeHealth) bool {
		age := float64(now.Unix()) - n.LatestQualityAt
		return n.Available && n.LatestDelayMS != nil && *n.LatestDelayMS >= 0 && n.LatestQualityAt > 0 && age >= 0 && age <= float64(window)
	}
	var active *nodeHealth
	for i := range nodes {
		if nodes[i].Selected {
			active = &nodes[i]
			break
		}
	}
	for i := range nodes {
		n := &nodes[i]
		n.LatencyDelta = nil
		switch {
		case active == nil || !fresh(*active) || *active.LatestDelayMS <= 0:
			n.DeltaReason = "active-measurement-missing-or-stale"
		case !fresh(*n):
			n.DeltaReason = "node-measurement-missing-or-stale"
		default:
			delta := 100 * float64(*active.LatestDelayMS-*n.LatestDelayMS) / float64(*active.LatestDelayMS)
			n.LatencyDelta = &delta
			n.DeltaReason = ""
		}
	}
}

func printHealth(paths daemon.Paths) error {
	type stats struct {
		Samples             int      `json:"samples"`
		AvailabilityPercent *float64 `json:"availability_percent"`
		MedianMS            *int     `json:"median_ms"`
		Loss                *float64 `json:"loss_percent"`
	}
	type state struct {
		HistoryTruncated   bool                                       `json:"history_truncated"`
		HistoryBudgetKiB   int                                        `json:"history_budget_kib"`
		HistoryUsageKiB    int                                        `json:"history_usage_kib"`
		RuntimeSelected    string                                     `json:"runtime_selected"`
		RuntimeConfirmed   bool                                       `json:"runtime_confirmed"`
		Mode               string                                     `json:"mode"`
		Selected           string                                     `json:"selected"`
		HealthyReserves    int                                        `json:"healthy_reserves"`
		CandidateCount     int                                        `json:"candidate_count"`
		CheckedAt          string                                     `json:"checked_at"`
		UnderlayFailure    string                                     `json:"underlay_failure"`
		CandidateSignature string                                     `json:"candidate_signature"`
		CandidateLabels    map[string]string                          `json:"candidate_labels"`
		HistoryDays        map[string]map[string]agentport.HistoryDay `json:"history_days"`
		PeriodStats        map[string]map[string]stats                `json:"period_stats"`
		AvailabilityOK     map[string]bool                            `json:"availability_ok"`
		LastProbeAt        map[string]float64                         `json:"last_probe_at"`
		LastGoodAt         map[string]float64                         `json:"last_good_at"`
		MedianDelayMS      map[string]*int                            `json:"median_delay_ms"`

		Samples map[string][]struct {
			At      float64 `json:"at"`
			OK      bool    `json:"ok"`
			DelayMS *int    `json:"delay_ms"`
		} `json:"samples"`

		FailureClass map[string]string `json:"failure_class"`
		DelayMS      map[string]*int   `json:"delay_ms"`
		ProbeLimits  struct {
			ActiveSeconds int `json:"active_seconds"`
			BackupSeconds int `json:"backup_seconds"`
		} `json:"probe_limits"`
	}
	values := map[string]state{}
	if body, err := os.ReadFile(filepath.Join(paths.RunRoot, "health-state", "selector-health.json")); err == nil {
		if err := json.Unmarshal(body, &values); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	result := make([]policyHealth, 0, len(values))
	for id, item := range values {
		entry := policyHealth{HistoryTruncated: item.HistoryTruncated, HistoryBudgetKiB: item.HistoryBudgetKiB, HistoryUsageKiB: item.HistoryUsageKiB, ID: strings.TrimPrefix(id, "resproxy-"), Mode: item.Mode,
			Selected: item.RuntimeSelected, HealthyReserves: item.HealthyReserves,
			CandidateCount: item.CandidateCount, CheckedAt: item.CheckedAt,
			UnderlayFailure: item.UnderlayFailure, Nodes: []nodeHealth{}}
		order := map[string]int{}
		for rank, nodeID := range strings.Split(item.CandidateSignature, "\n") {
			order[nodeID] = rank
		}
		for nodeID, label := range item.CandidateLabels {
			node := nodeHealth{ID: nodeID, Label: label, Selected: item.RuntimeConfirmed && nodeID == item.RuntimeSelected,
				Available: item.AvailabilityOK[nodeID], LastProbeAt: item.LastProbeAt[nodeID],
				FailureClass: item.FailureClass[nodeID], PeriodStats: map[string]nodeHealthStats{}}
			if rank, exists := order[nodeID]; exists {
				node.Order = &rank
			}
			if at := item.LastGoodAt[nodeID]; at > 0 {
				if delay := item.DelayMS[nodeID]; delay != nil && *delay >= 0 {
					node.CurrentMedianMS, node.LastGoodAt = delay, at
				}
			}
			if samples := item.Samples[nodeID]; len(samples) > 0 {
				latest := samples[len(samples)-1]
				if latest.OK && latest.DelayMS != nil && latest.At > 0 {
					node.LatestDelayMS = latest.DelayMS
					node.LatestQualityAt = latest.At
				}
			}

			for _, period := range []string{"today", "7d"} {
				if item.HistoryDays != nil {
					days := 1
					if period == "7d" {
						days = 7
					}
					summary := agentport.SummarizeHistoryDays(item.HistoryDays[nodeID], days, time.Now())
					node.PeriodStats[period] = nodeHealthStats{Samples: summary.Samples, Availability: summary.AvailabilityPercent, MedianMS: summary.MedianMS, Loss: summary.LossPercent}
					continue
				}

				if values := item.PeriodStats[period]; values != nil {
					summary := values[nodeID]
					node.PeriodStats[period] = nodeHealthStats{Samples: summary.Samples,
						Availability: summary.AvailabilityPercent, MedianMS: summary.MedianMS, Loss: summary.Loss}
				}
			}
			entry.Nodes = append(entry.Nodes, node)
		}
		setFreshLatencyDeltas(entry.Nodes, time.Now(), item.ProbeLimits.ActiveSeconds, item.ProbeLimits.BackupSeconds)
		sort.Slice(entry.Nodes, func(i, j int) bool {
			a, b := entry.Nodes[i], entry.Nodes[j]
			aStats, bStats := a.PeriodStats["today"], b.PeriodStats["today"]
			if aStats.Availability == nil || bStats.Availability == nil {
				return aStats.Availability != nil
			}
			if *aStats.Availability != *bStats.Availability {
				return *aStats.Availability > *bStats.Availability
			}
			if aStats.MedianMS != nil && bStats.MedianMS != nil && *aStats.MedianMS != *bStats.MedianMS {
				return *aStats.MedianMS < *bStats.MedianMS
			}
			return a.ID < b.ID
		})
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return json.NewEncoder(os.Stdout).Encode(result)
}

func running(paths daemon.Paths) bool {
	body, err := os.ReadFile(filepath.Join(paths.RunRoot, "daemon.pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil || pid < 1 {
		return false
	}
	comm, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
	return err == nil && strings.TrimSpace(string(comm)) == "resproxyd"
}

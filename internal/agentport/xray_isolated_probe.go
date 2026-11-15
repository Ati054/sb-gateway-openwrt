package agentport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Ati054/xroute-openwrt/internal/xray"
)

var errIsolatedProbeUnsupported = errors.New("isolated comparison is unsupported for this outbound")

// Preserve the real working observation, then compare fresh route requests.
// Ordinary transports use a separate generation; shared resources retain their
// real owner. Availability never enters this planning path.
func (runtime *xraySelectorRuntime) probeIsolatedPlanning(candidate string, request qualityProbeRequest) probeEvidence {
	evidence := runtime.probeWithQualityRequest(candidate, xrayQualityProbe, qualityProbeRequest{Measurement: qualityMeasurementCold, Start: request.Start})
	observation := &planningQualityEvidence{Measurement: request.Measurement, Deferred: true}
	evidence.Planning = observation
	defer request.WarmStart.arrive()
	if !evidence.OK || evidence.PrimaryQualityFailed || evidence.QualityUnmeasured {
		request.WarmStart.abortWarm()
		return evidence
	}
	parent := runtime.requestContext()
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	// All maps belong to this serial lane; only its scoped request context and
	// owned probe tag differ. Production selection and handlers are untouched.
	scoped := *runtime
	scoped.probeContext = ctx
	transport := "independent-handler"
	if request.Measurement == qualityMeasurementRoutePaired {
		var err error
		transport, err = runtime.planningTransport(candidate)
		if err != nil {
			request.WarmStart.abortWarm()
			return evidence
		}
	}
	observation.Transport = transport
	ownsHandler := transport == "independent-handler"
	defer func() {
		if !ownsHandler {
			return
		}
		cleanup, done := context.WithTimeout(context.WithoutCancel(parent), 2*time.Second)
		defer done()
		if err := scoped.retireIsolatedProbe(cleanup); err != nil {
			observation.Failed = true
		}
		runtime.probeRuntimeTag = scoped.probeRuntimeTag
	}()
	if err := scoped.selectProbeMode(scoped.probeSelectorName(), candidate, ownsHandler); err != nil {
		request.WarmStart.abortWarm()
		return evidence
	}
	if err := request.WarmStart.waitWarm(ctx); err != nil {
		return evidence
	}
	cancel() // Preparation and rendezvous must not consume the HEAD deadline.
	target := healthTargets[0]
	observation.StartedAt = time.Now()
	observation.Deferred = false
	_, err := requestThroughProxyContext(parent, runtime.opts.ProbeURL, target.url, http.MethodHead, 5*time.Second, 0, target.status)
	observation.ObservedAt = time.Now()
	observation.Failed = err != nil
	if err == nil {
		value := maxInt(1, int(observation.ObservedAt.Sub(observation.StartedAt).Milliseconds()))
		observation.DelayMS = &value
	}
	return evidence
}

// Apply owns the exclusive lock. Diagnostics must neither race that mutation
// nor wait indefinitely for it after the parent request has been cancelled.
func lockIsolatedMutation(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if xray.MutationMu.TryRLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (runtime *xraySelectorRuntime) retireIsolatedProbe(ctx context.Context) error {
	if runtime.probeRuntimeTag == "" {
		return nil
	}
	if err := lockIsolatedMutation(ctx); err != nil {
		return err
	}
	defer xray.MutationMu.RUnlock()
	scoped := *runtime
	scoped.probeContext = ctx
	err := scoped.removeOutbound(scoped.probeRuntimeTag)
	runtime.probeRuntimeTag = scoped.probeRuntimeTag
	return err
}

// A fresh HTTP/TLS request is available on every real route. A new Xray tag
// additionally isolates ordinary transports, but cannot duplicate a remote
// reverse session, helper process or referenced detour. Never remove those
// shared resources or substitute a direct outbound for an unavailable portal.
func (runtime *xraySelectorRuntime) planningTransport(candidate string) (string, error) {
	if candidate == "block" {
		return "", errIsolatedProbeUnsupported
	}
	if candidate == "direct-wan" {
		return "direct", nil
	}
	if runtime.isReverse(candidate) {
		return "shared-session", nil
	}
	if !runtime.pool.hasOutbound(candidate) {
		return "shared-session", nil
	}
	definition, err := runtime.pool.outboundDefinition(runtime.requestContext(), candidate)
	if err != nil {
		return "", err
	}
	var outbound map[string]json.RawMessage
	if err := json.Unmarshal(definition, &outbound); err != nil {
		return "", err
	}
	if _, exists := outbound["_resproxy_plugin"]; exists {
		return "shared-session", nil
	}
	for _, key := range []string{"proxySettings"} {
		if value := outbound[key]; len(value) > 0 && string(value) != "null" && string(value) != "{}" {
			return "shared-session", nil
		}
	}
	var protocol string
	if err := json.Unmarshal(outbound["protocol"], &protocol); err != nil {
		return "", err
	}
	if protocol == "wireguard" {
		return "shared-session", nil
	}
	var stream struct {
		Sockopt struct {
			DialerProxy string `json:"dialerProxy"`
		} `json:"sockopt"`
	}
	if value := outbound["streamSettings"]; len(value) > 0 {
		if err := json.Unmarshal(value, &stream); err != nil {
			return "", err
		}
		if stream.Sockopt.DialerProxy != "" {
			return "shared-session", nil
		}
	}
	return "independent-handler", nil
}

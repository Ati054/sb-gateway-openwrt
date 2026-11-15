package agentport

import "context"

// Scope belongs to a single operation. Background callbacks may service other
// policies while the quality lane is running; a mutable currentPolicy is unsafe.
type policyProbeScope struct {
	Policy, Selected, Member string
}

type availabilityProbeRequest struct {
	policyProbeScope
	Confirmation bool
	Background   bool
	Emergency    bool
	Context      context.Context
}
type policyScopedProbeRuntime interface {
	ProbeAvailabilityWithRequest(string, availabilityProbeRequest) probeEvidence
}

func callScopedAvailability(runtime selectorRuntime, node string, request availabilityProbeRequest) probeEvidence {
	if scoped, ok := runtime.(policyScopedProbeRuntime); ok {
		return scoped.ProbeAvailabilityWithRequest(node, request)
	}
	if request.Context != nil {
		if r, ok := runtime.(interface {
			ProbeAvailabilityContext(context.Context, string) probeEvidence
		}); ok {
			return r.ProbeAvailabilityContext(request.Context, node)
		}
	}
	if request.Confirmation {
		if r, ok := runtime.(activeConfirmationRuntime); ok {
			return r.ProbeAvailabilityConfirmation(node)
		}
	}
	if request.Background {
		if r, ok := runtime.(interface{ ProbeBackgroundAvailability(string) probeEvidence }); ok {
			return r.ProbeBackgroundAvailability(node)
		}
	}
	if request.Emergency {
		if r, ok := runtime.(emergencyCandidateRuntime); ok {
			return r.ProbeEmergencyAvailability(node)
		}
	}
	return runtime.ProbeAvailability(node)
}
func (runtime *xraySelectorRuntime) ProbeAvailabilityWithRequest(node string, request availabilityProbeRequest) probeEvidence {
	return runtime.policyProbe(request.policyProbeScope, request.Context, func(scoped *xraySelectorRuntime) probeEvidence {
		if request.Confirmation {
			return scoped.ProbeAvailabilityConfirmation(node)
		}
		if request.Emergency {
			return scoped.ProbeEmergencyAvailability(node)
		}
		return scoped.ProbeAvailability(node)
	})
}
func (runtime *responsiveSelectorRuntime) ProbeAvailabilityWithRequest(node string, request availabilityProbeRequest) probeEvidence {
	if request.Background {
		return runtime.probeBackgroundAvailabilityWithRequest(node, request)
	}
	return runtime.activeAvailabilityOperation(func() probeEvidence { return callScopedAvailability(runtime.selectorRuntime, node, request) })
}
func (controller *healthController) probeAvailability(policy, node string, request availabilityProbeRequest) probeEvidence {
	request.policyProbeScope = controller.probeScope(policy)
	return callScopedAvailability(controller.runtime, node, request)
}

// Selector identity is captured before preparation and checked after HTTP.
// A handler can remain installed after a different route has become current.
func (runtime *xraySelectorRuntime) policyProbe(scope policyProbeScope, ctx context.Context, operation func(*xraySelectorRuntime) probeEvidence) probeEvidence {
	policy := scope.Policy
	scoped := *runtime
	if ctx != nil {
		scoped.probeContext = ctx
	}
	scoped.probePolicy = policy
	scoped.probePolicySelected = scope.Selected
	var member string
	if policy != "" {
		var err error
		member, err = scoped.selectedRuntimeMember(policy)
		if err != nil {
			if scoped.requestContext().Err() != nil {
				return probeEvidence{Deferred: true}
			}
			return probeEvidence{LocalFailure: true, LocalError: err}
		}
		if scope.Member != "" && scope.Member != member {
			return probeEvidence{Deferred: true}
		}
		scoped.probePolicyMember = member
	}
	result := operation(&scoped)
	runtime.probeRuntimeTag = scoped.probeRuntimeTag
	if scoped.requestContext().Err() != nil {
		return probeEvidence{Deferred: true}
	}
	if policy != "" {
		current, err := scoped.selectedRuntimeMember(policy)
		if err != nil {
			if scoped.requestContext().Err() != nil {
				return probeEvidence{Deferred: true}
			}
			return probeEvidence{LocalFailure: true, LocalError: err}
		}
		if current != member {
			return probeEvidence{Deferred: true}
		}
	}
	return result
}

// The controller's last readback is an immutable expected path for this job.
// Its opaque tag remains meaningful even when a new definition has a new digest.
func (runtime *xraySelectorRuntime) probePolicyIdentity(policy string) string {
	return runtime.selectorMembers[policy]
}
func (runtime *responsiveSelectorRuntime) probePolicyIdentity(policy string) string {
	if r, ok := runtime.selectorRuntime.(interface{ probePolicyIdentity(string) string }); ok {
		return r.probePolicyIdentity(policy)
	}
	return ""
}
func (controller *healthController) probeScope(policy string) policyProbeScope {
	scope := policyProbeScope{Policy: policy}
	if item := controller.state[policy]; item != nil && item.RuntimeConfirmed {
		scope.Selected = item.RuntimeSelected
		if scope.Selected == "" {
			scope.Selected = item.Selected
		}
	}
	if r, ok := controller.runtime.(interface{ probePolicyIdentity(string) string }); ok {
		scope.Member = r.probePolicyIdentity(policy)
	}
	if scope.Member == "block" || scope.Member == "direct-wan" {
		scope.Selected = scope.Member
	}
	return scope
}

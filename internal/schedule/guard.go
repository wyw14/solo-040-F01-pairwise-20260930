package schedule

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

// DenyCode identifies why an amendment was refused.
type DenyCode string

const (
	DenyPlanNotFound      DenyCode = "plan_not_found"
	DenyPlanCancelled     DenyCode = "plan_cancelled"
	DenyDeviceAction      DenyCode = "device_action_started"
	DenyTransitInProgress DenyCode = "transit_in_progress"
	DenyVesselReleased    DenyCode = "vessel_released"
	DenyVersionConflict   DenyCode = "version_conflict"
)

// RevisionError is returned when a plan cannot be rewritten in place.
type RevisionError struct {
	Code   DenyCode
	Reason string
	Plan   model.ConvoyPlan
}

func (e *RevisionError) Error() string {
	return fmt.Sprintf("plan %s amendment rejected (%s): %s", e.Plan.ID, e.Code, e.Reason)
}

// The guard interfaces are satisfied by cycle.Coordinator, gate.Commands,
// safety.Barrier and signal.Store without schedule importing those packages.

type transitGuard interface {
	Current(string) (model.TransitCycle, bool)
}

type commandGuard interface {
	AnyForCycle(uuid.UUID) bool
}

type equipmentGuard interface {
	Pending(string) int
}

type signalGuard interface {
	Get(uuid.UUID) (model.SignalState, bool)
	RemoveIfDraft(vesselID uuid.UUID) bool
	Restore(state model.SignalState)
}

// Amendability is the operator-visible lock decision for a plan.
type Amendability struct {
	Amendable bool     `json:"amendable"`
	Code      DenyCode `json:"code,omitempty"`
	Reason    string   `json:"reason,omitempty"`
}

func (p *Planner) amendable(plan model.ConvoyPlan) Amendability {
	if plan.Cancelled {
		return Amendability{Code: DenyPlanCancelled, Reason: "plan has been cancelled"}
	}
	var cycleValue model.TransitCycle
	active := false
	if p.transits != nil {
		cycleValue, active = p.transits.Current(plan.ChamberID)
	}
	matching := active && !cycleValue.Stage.Terminal() && cycleValue.PlanID == plan.ID
	if matching {
		if p.commands != nil && p.commands.AnyForCycle(cycleValue.ID) {
			return Amendability{Code: DenyDeviceAction, Reason: "device action has already started for this lockage"}
		}
		if p.equipment != nil && p.equipment.Pending(plan.ChamberID) > 0 {
			return Amendability{Code: DenyDeviceAction, Reason: "equipment actions are pending in the chamber"}
		}
		if cycleValue.Stage != model.StagePlanned {
			return Amendability{Code: DenyTransitInProgress, Reason: "transit has reached stage " + string(cycleValue.Stage)}
		}
		if p.signals != nil {
			for _, vessel := range plan.Vessels {
				state, ok := p.signals.Get(vessel.ID)
				if ok && state.CycleID == cycleValue.ID && state.Aspect == model.SignalProceed {
					return Amendability{Code: DenyVesselReleased, Reason: "vessel " + vessel.Name + " has already received a proceed signal"}
				}
			}
		}
	}
	for _, entry := range p.admission.List(plan.ChamberID) {
		if entry.PlanID == plan.ID && entry.Released {
			return Amendability{Code: DenyVesselReleased, Reason: "vessel " + entry.Vessel.Name + " has already been released into the chamber"}
		}
	}
	return Amendability{Amendable: true}
}

// Amendable reports the lock decision without attempting a rewrite.
func (p *Planner) Amendable(planID uuid.UUID) (model.ConvoyPlan, Amendability) {
	plan, ok := p.Get(planID)
	if !ok {
		return model.ConvoyPlan{}, Amendability{Code: DenyPlanNotFound, Reason: "convoy plan not found"}
	}
	return plan, p.amendable(plan)
}

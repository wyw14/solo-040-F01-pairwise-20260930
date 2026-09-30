package schedule

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

// VesselRef identifies an existing vessel by ID or describes a new one.
type VesselRef struct {
	ID       uuid.UUID `json:"id,omitempty"`
	Name     string    `json:"name"`
	CallSign string    `json:"call_sign"`
	LengthM  float64   `json:"length_m"`
}

// Revision is the observable result of one successful amendment.
type Revision struct {
	Plan           model.ConvoyPlan   `json:"plan"`
	Revision       model.PlanRevision `json:"revision"`
	QueueLength    int                `json:"queue_length"`
	SignalsRemoved int                `json:"signals_removed"`
}

// Revise rewrites a plan's vessel list while the lockage has not entered the
// chamber. The plan identity is preserved; every accepted rewrite raises the
// version and stores an immutable snapshot.
func (p *Planner) Revise(ctx context.Context, planID uuid.UUID, expectedVersion uint64, refs []VesselRef, note string) (Revision, error) {
	plan, ok := p.Get(planID)
	if !ok {
		return Revision{}, &RevisionError{Code: DenyPlanNotFound, Reason: "convoy plan not found"}
	}
	if decision := p.amendable(plan); !decision.Amendable {
		p.recordDenial(ctx, plan, decision.Code, decision.Reason)
		return Revision{}, &RevisionError{Code: decision.Code, Reason: decision.Reason, Plan: plan}
	}
	if expectedVersion != 0 && expectedVersion != plan.Version {
		reason := fmt.Sprintf("plan is at version %d, edit was based on version %d", plan.Version, expectedVersion)
		p.recordDenial(ctx, plan, DenyVersionConflict, reason)
		return Revision{}, &RevisionError{Code: DenyVersionConflict, Reason: reason, Plan: plan}
	}
	vessels, err := resolveVessels(plan, refs)
	if err != nil {
		return Revision{}, err
	}

	previous := plan.Clone()
	summary := diffVessels(previous.Vessels, vessels)
	plan.Vessels = vessels
	plan.Version++
	plan.UpdatedAt = p.now().UTC()

	oldEntries, err := p.admission.ReplacePlan(plan.ChamberID, plan)
	if err != nil {
		return Revision{}, &RevisionError{Code: DenyVesselReleased, Reason: err.Error(), Plan: previous}
	}
	_ = oldEntries // released entries cannot exist here; rollback re-splices from previous
	removedSignals := p.removeDraftSignals(plan.ChamberID, previous.Vessels, vessels)

	revision := model.PlanRevision{
		Version: plan.Version, Note: note, ChangedAt: plan.UpdatedAt,
		Vessels: plan.Clone().Vessels, Summary: summary,
	}
	p.commitRevision(plan, revision)
	if _, err := p.recorder.Record(ctx, plan.ChamberID, model.EventPlanRevised, plan.ID, plan.Version, map[string]any{
		"plan_id":         plan.ID,
		"version":         plan.Version,
		"note":            note,
		"vessels":         plan.Vessels,
		"summary":         summary,
		"signals_removed": len(removedSignals),
	}); err != nil {
		p.rollback(previous, removedSignals)
		return Revision{}, err
	}
	return Revision{
		Plan: plan.Clone(), Revision: revision.Clone(),
		QueueLength:    len(p.admission.List(plan.ChamberID)),
		SignalsRemoved: len(removedSignals),
	}, nil
}

func (p *Planner) commitRevision(plan model.ConvoyPlan, revision model.PlanRevision) {
	p.mu.Lock()
	p.plans[plan.ID] = plan.Clone()
	p.revisions[plan.ID] = append(p.revisions[plan.ID], revision.Clone())
	p.mu.Unlock()
}

// rollback reverses every side effect of a failed amendment, in reverse order.
func (p *Planner) rollback(previous model.ConvoyPlan, removedSignals []model.SignalState) {
	if _, err := p.admission.ReplacePlan(previous.ChamberID, previous); err != nil {
		// The old block cannot be re-spliced; surface it in the journal rather
		// than silently losing queue membership.
		_, _ = p.recorder.Record(context.Background(), previous.ChamberID, model.EventReplayWarning, previous.ID, previous.Version, map[string]any{
			"rollback": "queue restore failed", "error": err.Error(),
		})
	}
	for index := range removedSignals {
		if p.signals != nil {
			p.signals.Restore(removedSignals[index])
		}
	}
	p.mu.Lock()
	p.plans[previous.ID] = previous.Clone()
	if history := p.revisions[previous.ID]; len(history) > 1 {
		p.revisions[previous.ID] = history[:len(history)-1]
	}
	p.mu.Unlock()
}

// Revisions returns the stored version snapshots, oldest first.
func (p *Planner) Revisions(planID uuid.UUID) []model.PlanRevision {
	p.mu.RLock()
	revisions := append([]model.PlanRevision(nil), p.revisions[planID]...)
	p.mu.RUnlock()
	for index := range revisions {
		revisions[index] = revisions[index].Clone()
	}
	return revisions
}

func resolveVessels(plan model.ConvoyPlan, refs []VesselRef) ([]model.Vessel, error) {
	if len(refs) == 0 {
		return nil, errors.New("plan must keep at least one vessel")
	}
	existing := make(map[uuid.UUID]model.Vessel, len(plan.Vessels))
	for _, vessel := range plan.Vessels {
		existing[vessel.ID] = vessel
	}
	vessels := make([]model.Vessel, 0, len(refs))
	seen := make(map[uuid.UUID]struct{}, len(refs))
	for _, ref := range refs {
		if ref.ID != uuid.Nil {
			vessel, ok := existing[ref.ID]
			if !ok {
				return nil, fmt.Errorf("vessel %s is not part of plan %s", ref.ID, plan.ID)
			}
			if _, dup := seen[vessel.ID]; dup {
				return nil, fmt.Errorf("vessel %s appears more than once", vessel.Name)
			}
			seen[vessel.ID] = struct{}{}
			vessels = append(vessels, vessel)
			continue
		}
		vessel, err := model.NewVessel(ref.Name, ref.CallSign, ref.LengthM)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[vessel.ID]; dup {
			return nil, fmt.Errorf("vessel %s appears more than once", vessel.Name)
		}
		seen[vessel.ID] = struct{}{}
		vessels = append(vessels, vessel)
	}
	return vessels, nil
}

func diffVessels(before, after []model.Vessel) model.RevisionSummary {
	oldIndex := make(map[uuid.UUID]int, len(before))
	for index, vessel := range before {
		oldIndex[vessel.ID] = index
	}
	newIDs := make(map[uuid.UUID]struct{}, len(after))
	var summary model.RevisionSummary
	for index, vessel := range after {
		newIDs[vessel.ID] = struct{}{}
		if prior, ok := oldIndex[vessel.ID]; ok && prior != index {
			summary.Moved++
		}
		if _, ok := oldIndex[vessel.ID]; !ok {
			summary.Added++
		}
	}
	for _, vessel := range before {
		if _, ok := newIDs[vessel.ID]; !ok {
			summary.Removed++
		}
	}
	return summary
}

func (p *Planner) removeDraftSignals(chamberID string, before, after []model.Vessel) []model.SignalState {
	if p.signals == nil {
		return nil
	}
	keep := make(map[uuid.UUID]struct{}, len(after))
	for _, vessel := range after {
		keep[vessel.ID] = struct{}{}
	}
	var removed []model.SignalState
	for _, vessel := range before {
		if _, retained := keep[vessel.ID]; retained {
			continue
		}
		if state, ok := p.signals.Get(vessel.ID); ok {
			if p.signals.RemoveIfDraft(vessel.ID) {
				removed = append(removed, state)
				_, _ = p.recorder.Record(context.Background(), chamberID, model.EventSignalChanged, uuid.Nil, 0, map[string]any{
					"vessel_id": vessel.ID, "draft_removed": true,
				})
			}
		}
	}
	return removed
}

func (p *Planner) recordDenial(ctx context.Context, plan model.ConvoyPlan, code DenyCode, reason string) {
	if p.recorder == nil {
		return
	}
	_, _ = p.recorder.Record(ctx, plan.ChamberID, model.EventPlanReviseDenied, plan.ID, plan.Version, map[string]any{
		"plan_id": plan.ID, "version": plan.Version, "code": string(code), "reason": reason,
	})
}

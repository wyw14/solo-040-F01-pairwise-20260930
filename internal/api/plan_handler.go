package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/cycle"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/schedule"
	"github.com/wyw14/cry-96/internal/signal"
)

type vesselInput struct {
	ID       string  `json:"id,omitempty"`
	Name     string  `json:"name"`
	CallSign string  `json:"call_sign"`
	LengthM  float64 `json:"length_m"`
}

type planInput struct {
	ChamberID string          `json:"chamber_id"`
	Direction model.Direction `json:"direction"`
	Vessels   []vesselInput   `json:"vessels"`
}

type reviseInput struct {
	ExpectedVersion uint64        `json:"expected_version"`
	Vessels         []vesselInput `json:"vessels"`
	Note            string        `json:"note"`
}

// planView pairs a plan with its amendment lock state.
type planView struct {
	model.ConvoyPlan
	Amendable    bool   `json:"amendable"`
	RejectCode   string `json:"reject_code,omitempty"`
	RejectReason string `json:"reject_reason,omitempty"`
}

func toVesselRefs(items []vesselInput) ([]schedule.VesselRef, error) {
	refs := make([]schedule.VesselRef, 0, len(items))
	for _, item := range items {
		ref := schedule.VesselRef{Name: item.Name, CallSign: item.CallSign, LengthM: item.LengthM}
		if item.ID != "" {
			id, err := uuid.Parse(item.ID)
			if err != nil {
				return nil, err
			}
			ref.ID = id
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func createPlanHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input planInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		vessels := make([]model.Vessel, 0, len(input.Vessels))
		for _, item := range input.Vessels {
			vessel, err := model.NewVessel(item.Name, item.CallSign, item.LengthM)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			vessels = append(vessels, vessel)
		}
		plan, err := runtime.Schedule.Create(r.Context(), input.ChamberID, input.Direction, vessels)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		cycleValue, err := runtime.CycleService.Start(r.Context(), plan)
		if err != nil {
			if errors.Is(err, cycle.ErrChamberBusy) {
				// The plan is safely queued behind the running lockage; it can be
				// amended until its own transit starts.
				writeJSON(w, http.StatusCreated, map[string]any{
					"plan": plan, "queued": true, "cycle": nil,
				})
				return
			}
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"plan": plan, "queued": false, "cycle": cycleValue})
	}
}

func plansHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result := make(map[string][]planView, len(runtime.Chambers))
		for _, chamberID := range runtime.Chambers {
			for _, plan := range runtime.Schedule.Active(chamberID) {
				_, decision := runtime.Planner.Amendable(plan.ID)
				result[chamberID] = append(result[chamberID], planView{
					ConvoyPlan:   plan,
					Amendable:    decision.Amendable,
					RejectCode:   string(decision.Code),
					RejectReason: decision.Reason,
				})
			}
			if result[chamberID] == nil {
				result[chamberID] = []planView{}
			}
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func planDetailHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID, err := uuid.Parse(chi.URLParam(r, "planID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		plan, decision, revisions, ok := runtime.Schedule.Detail(planID)
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("plan not found"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"plan": planView{
				ConvoyPlan:   plan,
				Amendable:    decision.Amendable,
				RejectCode:   string(decision.Code),
				RejectReason: decision.Reason,
			},
			"revisions": revisions,
		})
	}
}

func revisePlanHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID, err := uuid.Parse(chi.URLParam(r, "planID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var input reviseInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if len(input.Vessels) == 0 {
			writeError(w, http.StatusBadRequest, errors.New("plan must keep at least one vessel"))
			return
		}
		refs, err := toVesselRefs(input.Vessels)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := runtime.Schedule.Revise(r.Context(), planID, input.ExpectedVersion, refs, input.Note)
		if err != nil {
			var denied *schedule.RevisionError
			if errors.As(err, &denied) {
				if denied.Code == schedule.DenyPlanNotFound {
					writeError(w, http.StatusNotFound, err)
					return
				}
				writeRevisionError(w, http.StatusConflict, denied)
				return
			}
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func writeRevisionError(w http.ResponseWriter, status int, denied *schedule.RevisionError) {
	writeJSON(w, status, map[string]any{
		"error":     denied.Error(),
		"code":      string(denied.Code),
		"plan_id":   denied.Plan.ID,
		"version":   denied.Plan.Version,
		"amendable": false,
		"reason":    map[string]any{"code": string(denied.Code), "message": denied.Reason},
	})
}

func publishSignalsHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID, err := uuid.Parse(chi.URLParam(r, "planID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		plan, ok := runtime.Planner.Get(planID)
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("plan not found"))
			return
		}
		cycleValue, ok := runtime.Cycles.Current(plan.ChamberID)
		if !ok {
			writeError(w, http.StatusConflict, errors.New("active transit not found"))
			return
		}
		publication, err := runtime.Schedule.Publish(r.Context(), planID, cycleValue)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusAccepted, publication)
	}
}

func cancelPlanHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID, err := uuid.Parse(chi.URLParam(r, "planID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		plan, ok := runtime.Planner.Get(planID)
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("plan not found"))
			return
		}
		cycleValue, ok := runtime.Cycles.Current(plan.ChamberID)
		publication := signal.Publication{
			CycleID: cycleValue.ID, Generation: cycleValue.Generation,
			Previous: make(map[uuid.UUID]model.SignalState),
		}
		if ok {
			for _, vessel := range plan.Vessels {
				if _, exists := runtime.SignalStore.Get(vessel.ID); exists {
					publication.Published = append(publication.Published, vessel.ID)
				}
			}
		}
		result, err := runtime.Schedule.Cancel(r.Context(), planID, "operator cancellation", publication)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		if ok {
			_, _ = runtime.Cycles.Cancel(r.Context(), plan.ChamberID, "operator cancellation")
		}
		writeJSON(w, http.StatusOK, result)
	}
}

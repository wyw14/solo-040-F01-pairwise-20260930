package schedule

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/signal"
)

// Service exposes planning and signal-publication workflows to the API.
type Service struct {
	planner   *Planner
	publisher *signal.Publisher
}

func NewService(planner *Planner, publisher *signal.Publisher) *Service {
	return &Service{planner: planner, publisher: publisher}
}

func (s *Service) Create(ctx context.Context, chamberID string, direction model.Direction, vessels []model.Vessel) (model.ConvoyPlan, error) {
	return s.planner.Create(ctx, chamberID, direction, vessels)
}

func (s *Service) Publish(ctx context.Context, planID uuid.UUID, cycle model.TransitCycle) (signal.Publication, error) {
	plan, ok := s.planner.Get(planID)
	if !ok || plan.Cancelled {
		return signal.Publication{}, errors.New("active convoy plan not found")
	}
	if plan.ChamberID != cycle.ChamberID || plan.ID != cycle.PlanID {
		return signal.Publication{}, errors.New("plan and transit do not match")
	}
	return s.publisher.Publish(ctx, cycle, plan.Vessels, model.SignalProceed)
}

func (s *Service) Cancel(ctx context.Context, planID uuid.UUID, note string, publication signal.Publication) (Cancellation, error) {
	return s.planner.Cancel(ctx, planID, note, s.publisher, publication)
}

func (s *Service) Revise(ctx context.Context, planID uuid.UUID, expectedVersion uint64, vessels []VesselRef, note string) (Revision, error) {
	return s.planner.Revise(ctx, planID, expectedVersion, vessels, note)
}

func (s *Service) Detail(planID uuid.UUID) (model.ConvoyPlan, Amendability, []model.PlanRevision, bool) {
	plan, decision := s.planner.Amendable(planID)
	if decision.Code == DenyPlanNotFound {
		return model.ConvoyPlan{}, Amendability{}, nil, false
	}
	return plan, decision, s.planner.Revisions(planID), true
}

func (s *Service) PlannerView() *Planner { return s.planner }

func (s *Service) Active(chamberID string) []model.ConvoyPlan {
	return s.planner.Active(chamberID)
}

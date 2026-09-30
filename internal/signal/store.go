package signal

import (
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

// Store keeps the latest field aspect for each vessel.
type Store struct {
	mu     sync.RWMutex
	states map[uuid.UUID]model.SignalState
}

func NewStore() *Store { return &Store{states: make(map[uuid.UUID]model.SignalState)} }

func (s *Store) Set(vesselID, cycleID uuid.UUID, generation uint64, aspect model.SignalAspect, now time.Time) model.SignalState {
	state := model.SignalState{
		VesselID: vesselID, CycleID: cycleID, Generation: generation,
		Aspect: aspect, UpdatedAt: now.UTC(),
	}
	s.mu.Lock()
	s.states[vesselID] = state
	s.mu.Unlock()
	return state
}

func (s *Store) Get(vesselID uuid.UUID) (model.SignalState, bool) {
	s.mu.RLock()
	state, ok := s.states[vesselID]
	s.mu.RUnlock()
	return state, ok
}

func (s *Store) Remove(vesselID uuid.UUID) {
	s.mu.Lock()
	delete(s.states, vesselID)
	s.mu.Unlock()
}

// Restore reinstates a previously observed signal state verbatim.
func (s *Store) Restore(state model.SignalState) {
	s.mu.Lock()
	s.states[state.VesselID] = state
	s.mu.Unlock()
}

// RemoveIfDraft deletes a vessel's signal only while it has not been given a
// proceed aspect. Published proceed signals are never removed here.
func (s *Store) RemoveIfDraft(vesselID uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[vesselID]
	if !ok || state.Aspect == model.SignalProceed {
		return false
	}
	delete(s.states, vesselID)
	return true
}

func (s *Store) List() []model.SignalState {
	s.mu.RLock()
	result := make([]model.SignalState, 0, len(s.states))
	for _, state := range s.states {
		result = append(result, state)
	}
	s.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		return result[i].VesselID.String() < result[j].VesselID.String()
	})
	return result
}

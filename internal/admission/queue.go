package admission

import (
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

type QueueEntry struct {
	Vessel   model.Vessel `json:"vessel"`
	PlanID   uuid.UUID    `json:"plan_id"`
	Released bool         `json:"released"`
}

// Queue preserves FIFO order while supporting convoy cancellation recovery.
type Queue struct {
	mu      sync.RWMutex
	entries map[string][]QueueEntry
}

func NewQueue() *Queue { return &Queue{entries: make(map[string][]QueueEntry)} }

func (q *Queue) Add(chamberID string, planID uuid.UUID, vessels []model.Vessel) error {
	if chamberID == "" || planID == uuid.Nil || len(vessels) == 0 {
		return errors.New("queue addition requires chamber, plan and vessels")
	}
	q.mu.Lock()
	for _, vessel := range vessels {
		q.entries[chamberID] = append(q.entries[chamberID], QueueEntry{Vessel: vessel, PlanID: planID})
	}
	q.mu.Unlock()
	return nil
}

func (q *Queue) Peek(chamberID string) (QueueEntry, bool) {
	q.mu.RLock()
	entries := q.entries[chamberID]
	q.mu.RUnlock()
	if len(entries) == 0 {
		return QueueEntry{}, false
	}
	return entries[0], true
}

func (q *Queue) MarkReleased(chamberID string, vesselID uuid.UUID) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for index := range q.entries[chamberID] {
		if q.entries[chamberID][index].Vessel.ID == vesselID {
			q.entries[chamberID][index].Released = true
			return true
		}
	}
	return false
}

func (q *Queue) RemovePlan(chamberID string, planID uuid.UUID) []QueueEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := q.entries[chamberID]
	kept := entries[:0]
	var removed []QueueEntry
	for _, entry := range entries {
		if entry.PlanID == planID {
			removed = append(removed, entry)
		} else {
			kept = append(kept, entry)
		}
	}
	q.entries[chamberID] = append([]QueueEntry(nil), kept...)
	return removed
}

// ReplacePlan swaps a queued plan's vessel block in place, keeping its
// position relative to other plans. Released entries block the swap.
func (q *Queue) ReplacePlan(chamberID string, planID uuid.UUID, vessels []model.Vessel) ([]QueueEntry, error) {
	if chamberID == "" || planID == uuid.Nil || len(vessels) == 0 {
		return nil, errors.New("queue replacement requires chamber, plan and vessels")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := q.entries[chamberID]
	kept := make([]QueueEntry, 0, len(entries))
	insertAt := -1
	var removed []QueueEntry
	for _, entry := range entries {
		if entry.PlanID == planID {
			if insertAt == -1 {
				insertAt = len(kept)
			}
			if entry.Released {
				return nil, errors.New("released vessels cannot be reordered")
			}
			removed = append(removed, entry)
		} else {
			kept = append(kept, entry)
		}
	}
	if insertAt == -1 {
		return nil, errors.New("plan is not queued")
	}
	revised := make([]QueueEntry, 0, len(kept)+len(vessels))
	revised = append(revised, kept[:insertAt]...)
	for _, vessel := range vessels {
		revised = append(revised, QueueEntry{Vessel: vessel, PlanID: planID})
	}
	revised = append(revised, kept[insertAt:]...)
	q.entries[chamberID] = revised
	return removed, nil
}

func (q *Queue) Restore(chamberID string, entries []QueueEntry) {
	q.mu.Lock()
	current := q.entries[chamberID]
	restored := make([]QueueEntry, 0, len(entries)+len(current))
	for _, entry := range entries {
		entry.Released = false
		restored = append(restored, entry)
	}
	q.entries[chamberID] = append(restored, current...)
	q.mu.Unlock()
}

func (q *Queue) List(chamberID string) []QueueEntry {
	q.mu.RLock()
	result := append([]QueueEntry(nil), q.entries[chamberID]...)
	q.mu.RUnlock()
	return result
}

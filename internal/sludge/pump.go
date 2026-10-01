package sludge

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/lockout"
	"github.com/wyw14/cry-97/internal/model"
)

type Pump struct {
	ID              string           `json:"id"`
	LineID          model.LineID     `json:"line_id"`
	Kind            model.DeviceKind `json:"kind"`
	Running         bool             `json:"running"`
	Suspended       bool             `json:"suspended"`
	Locked          bool             `json:"locked"`
	Primed          bool             `json:"primed"`
	Flow            float64          `json:"flow"`
	OwnerSequence   uuid.UUID        `json:"owner_sequence,omitempty"`
	OwnerGeneration uint64           `json:"owner_generation"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type PumpController struct {
	mu       sync.RWMutex
	pumps    map[string]Pump
	lockouts *lockout.Book
}

func NewPumpController(pumps []Pump, book *lockout.Book) (*PumpController, error) {
	if len(pumps) < 2 {
		return nil, errors.New("at least two return pumps are required")
	}
	if book == nil {
		return nil, errors.New("pump controller requires the lockout book")
	}
	controller := &PumpController{pumps: make(map[string]Pump, len(pumps)), lockouts: book}
	for _, pump := range pumps {
		if pump.ID == "" || pump.LineID == "" {
			return nil, errors.New("pump identity is incomplete")
		}
		controller.pumps[pump.ID] = pump
	}
	return controller, nil
}

func (c *PumpController) Assign(pumpID string, sequenceID uuid.UUID, generation uint64, now time.Time) (Pump, error) {
	if c.lockouts.IsLocked(pumpID) {
		return Pump{}, errors.New("pump assignment rejected: pump is locked out for field maintenance")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	pump, ok := c.pumps[pumpID]
	if !ok || sequenceID == uuid.Nil || generation == 0 {
		return Pump{}, errors.New("pump assignment is invalid")
	}
	pump.OwnerSequence = sequenceID
	pump.OwnerGeneration = generation
	pump.Primed = false
	pump.Running = false
	pump.Suspended = false
	pump.Flow = 0
	pump.UpdatedAt = now.UTC()
	c.pumps[pumpID] = pump
	return pump, nil
}

func (c *PumpController) Ready(pumpID string, sequenceID uuid.UUID, generation uint64, flow float64, now time.Time) (Pump, error) {
	if c.lockouts.IsLocked(pumpID) {
		return Pump{}, errors.New("pump ready confirmation rejected: pump is locked out for field maintenance")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	pump, ok := c.pumps[pumpID]
	if !ok {
		return Pump{}, errors.New("return pump is not found")
	}
	if pump.OwnerSequence != sequenceID || pump.OwnerGeneration != generation {
		return Pump{}, errors.New("pump ready confirmation is stale")
	}
	if flow <= 0 {
		return Pump{}, errors.New("pump ready confirmation has no flow")
	}
	pump.Primed = true
	pump.Running = true
	pump.Suspended = false
	pump.Flow = flow
	pump.UpdatedAt = now.UTC()
	c.pumps[pumpID] = pump
	return pump, nil
}

func (c *PumpController) StopOwned(pumpID string, sequenceID uuid.UUID, generation uint64, now time.Time) (Pump, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pump, ok := c.pumps[pumpID]
	if !ok {
		return Pump{}, errors.New("return pump is not found")
	}
	if pump.OwnerSequence != sequenceID || pump.OwnerGeneration != generation {
		return Pump{}, errors.New("pump ownership changed before stop")
	}
	pump.Running = false
	pump.Suspended = false
	pump.Flow = 0
	pump.UpdatedAt = now.UTC()
	c.pumps[pumpID] = pump
	return pump, nil
}

// Suspend 按安全规则挂起在途运行的回流泵：停机但保留属主和灌注信息，
// 以便检修复役后由在途工序决定是否重新确认流量，而不是静默恢复运行。
func (c *PumpController) Suspend(pumpID string, now time.Time) (Pump, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pump, ok := c.pumps[pumpID]
	if !ok {
		return Pump{}, false, errors.New("return pump is not found")
	}
	changed := pump.Running || pump.Flow != 0
	pump.Running = false
	pump.Suspended = true
	pump.Flow = 0
	pump.UpdatedAt = now.UTC()
	c.pumps[pumpID] = pump
	return pump, changed, nil
}

// ClearSuspension 在泵复役后解除挂起标志；泵保持停止，等待在途工序重新确认流量。
func (c *PumpController) ClearSuspension(pumpID string, now time.Time) (Pump, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pump, ok := c.pumps[pumpID]
	if !ok {
		return Pump{}, false, errors.New("return pump is not found")
	}
	changed := pump.Suspended
	pump.Suspended = false
	pump.UpdatedAt = now.UTC()
	c.pumps[pumpID] = pump
	return pump, changed, nil
}

func (c *PumpController) Snapshot() []Pump {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]Pump, 0, len(c.pumps))
	for _, pump := range c.pumps {
		pump.Locked = c.lockouts.IsLocked(pump.ID)
		result = append(result, pump)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

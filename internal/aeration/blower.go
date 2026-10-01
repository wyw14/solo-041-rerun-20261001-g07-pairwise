package aeration

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/cry-97/internal/lockout"
	"github.com/wyw14/cry-97/internal/model"
)

type Blower struct {
	ID        string    `json:"id"`
	BasinID   string    `json:"basin_id"`
	Frequency float64   `json:"frequency"`
	Running   bool      `json:"running"`
	Locked    bool      `json:"locked"`
	UpdatedAt time.Time `json:"updated_at"`
}

type BlowerFleet struct {
	mu       sync.RWMutex
	blowers  map[string]Blower
	lockouts *lockout.Book
}

func NewBlowerFleet(blowers []Blower, book *lockout.Book) (*BlowerFleet, error) {
	if len(blowers) == 0 {
		return nil, errors.New("at least one blower is required")
	}
	if book == nil {
		return nil, errors.New("blower fleet requires the lockout book")
	}
	fleet := &BlowerFleet{blowers: make(map[string]Blower, len(blowers)), lockouts: book}
	for _, blower := range blowers {
		if blower.ID == "" || blower.BasinID == "" {
			return nil, errors.New("blower identity is required")
		}
		fleet.blowers[blower.ID] = blower
	}
	return fleet, nil
}

func (f *BlowerFleet) Apply(command model.DeviceCommand, now time.Time) (Blower, error) {
	if f.lockouts.IsLocked(command.DeviceID) && !lockout.IsSafetyAction(command.Action) {
		return Blower{}, errors.New("blower command rejected: blower is locked out for field maintenance")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	blower, ok := f.blowers[command.DeviceID]
	if !ok || command.Kind != model.DeviceBlower {
		return Blower{}, errors.New("blower command target is unknown")
	}
	if command.Setpoint < 0 || command.Setpoint > 60 {
		return Blower{}, errors.New("blower frequency is outside operating range")
	}
	blower.Frequency = command.Setpoint
	blower.Running = command.Setpoint > 0 && !f.lockouts.IsLocked(command.DeviceID)
	blower.UpdatedAt = now.UTC()
	f.blowers[blower.ID] = blower
	return blower, nil
}

// ForceStop 按安全规则停机：封锁挂牌后即使存在在途运行指令也强制风机停机。
func (f *BlowerFleet) ForceStop(deviceID string, now time.Time) (Blower, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	blower, ok := f.blowers[deviceID]
	if !ok {
		return Blower{}, false, errors.New("blower is not found")
	}
	changed := blower.Running || blower.Frequency != 0
	blower.Running = false
	blower.Frequency = 0
	blower.UpdatedAt = now.UTC()
	f.blowers[deviceID] = blower
	return blower, changed, nil
}

func (f *BlowerFleet) Snapshot() []Blower {
	f.mu.RLock()
	defer f.mu.RUnlock()
	result := make([]Blower, 0, len(f.blowers))
	for _, blower := range f.blowers {
		blower.Locked = f.lockouts.IsLocked(blower.ID)
		result = append(result, blower)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

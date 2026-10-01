package discharge

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/lockout"
	"github.com/wyw14/cry-97/internal/model"
)

type Valve struct {
	LineID    model.LineID     `json:"line_id"`
	Kind      model.DeviceKind `json:"kind"`
	Open      bool             `json:"open"`
	Locked    bool             `json:"locked"`
	PermitID  uuid.UUID        `json:"permit_id,omitempty"`
	UpdatedAt time.Time        `json:"updated_at"`
}

type ValveController struct {
	mu        sync.RWMutex
	valves    map[model.LineID]Valve
	deviceIDs map[model.LineID]string
	lockouts  *lockout.Book
}

func NewValveController(lines []model.LineID, book *lockout.Book) (*ValveController, error) {
	if len(lines) == 0 {
		return nil, errors.New("discharge lines are required")
	}
	if book == nil {
		return nil, errors.New("valve controller requires the lockout book")
	}
	controller := &ValveController{
		valves:    make(map[model.LineID]Valve, len(lines)),
		deviceIDs: make(map[model.LineID]string, len(lines)),
		lockouts:  book,
	}
	for _, lineID := range lines {
		if lineID == "" {
			return nil, errors.New("discharge line is empty")
		}
		deviceID := "valve-discharge-" + string(lineID)
		controller.valves[lineID] = Valve{LineID: lineID, Kind: model.DeviceValve}
		controller.deviceIDs[lineID] = deviceID
	}
	return controller, nil
}

// DeviceID 返回处理线排水阀的稳定设备标识。
func (c *ValveController) DeviceID(lineID model.LineID) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.deviceIDs[lineID]
}

// LineForDevice 反查排水阀设备所属处理线，找不到返回空。
func (c *ValveController) LineForDevice(deviceID string) model.LineID {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for lineID, id := range c.deviceIDs {
		if id == deviceID {
			return lineID
		}
	}
	return ""
}

func (c *ValveController) Open(lineID model.LineID, permitID uuid.UUID, now time.Time) error {
	if c.lockouts.IsLocked(c.DeviceID(lineID)) {
		return errors.New("discharge valve open rejected: valve is locked out for field maintenance")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	valve, ok := c.valves[lineID]
	if !ok || permitID == uuid.Nil {
		return errors.New("discharge valve target is invalid")
	}
	valve.Open = true
	valve.PermitID = permitID
	valve.UpdatedAt = now.UTC()
	c.valves[lineID] = valve
	return nil
}

func (c *ValveController) Hold(lineID model.LineID, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	valve, ok := c.valves[lineID]
	if !ok {
		return errors.New("discharge valve is not found")
	}
	valve.Open = false
	valve.PermitID = uuid.Nil
	valve.UpdatedAt = now.UTC()
	c.valves[lineID] = valve
	return nil
}

// ForceClose 按安全规则关闭排水阀并摘除许可关联（封锁挂牌时调用）。
func (c *ValveController) ForceClose(lineID model.LineID, now time.Time) (Valve, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	valve, ok := c.valves[lineID]
	if !ok {
		return Valve{}, false, errors.New("discharge valve is not found")
	}
	changed := valve.Open || valve.PermitID != uuid.Nil
	valve.Open = false
	valve.PermitID = uuid.Nil
	valve.UpdatedAt = now.UTC()
	c.valves[lineID] = valve
	return valve, changed, nil
}

func (c *ValveController) Snapshot() []Valve {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]Valve, 0, len(c.valves))
	for _, valve := range c.valves {
		valve.Locked = c.lockouts.IsLocked(c.deviceIDs[valve.LineID])
		result = append(result, valve)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LineID < result[j].LineID })
	return result
}

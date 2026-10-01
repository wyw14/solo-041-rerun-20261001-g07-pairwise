package process

import (
	"errors"
	"sort"
	"sync"

	"github.com/wyw14/cry-97/internal/model"
)

// DeviceInfo 描述一台受控设备的身份：类型与所属处理线。
type DeviceInfo struct {
	DeviceID string           `json:"device_id"`
	Kind     model.DeviceKind `json:"kind"`
	LineID   model.LineID     `json:"line_id"`
}

// DeviceRegistry 维护设备标识到设备身份的映射，供现场封锁按设备 ID 定位。
type DeviceRegistry struct {
	mu      sync.RWMutex
	devices map[string]DeviceInfo
}

func NewDeviceRegistry() *DeviceRegistry {
	return &DeviceRegistry{devices: make(map[string]DeviceInfo)}
}

func (r *DeviceRegistry) Register(info DeviceInfo) error {
	if info.DeviceID == "" || info.LineID == "" || info.Kind == "" {
		return errors.New("device registry entry is incomplete")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.devices[info.DeviceID]; exists {
		return errors.New("device is already registered")
	}
	r.devices[info.DeviceID] = info
	return nil
}

func (r *DeviceRegistry) Lookup(deviceID string) (DeviceInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.devices[deviceID]
	return info, ok
}

func (r *DeviceRegistry) All() []DeviceInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]DeviceInfo, 0, len(r.devices))
	for _, info := range r.devices {
		result = append(result, info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].DeviceID < result[j].DeviceID })
	return result
}

package process

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/cry-97/internal/lockout"
	"github.com/wyw14/cry-97/internal/model"
)

// safetyActions 是封锁期间仍然允许的安全类指令（停机、关阀、保位）。
func isSafetyAction(action string) bool {
	return lockout.IsSafetyAction(action)
}

type DeviceBus struct {
	mu       sync.RWMutex
	commands map[string]model.DeviceCommand
	lockouts *lockout.Book
}

func NewDeviceBus(book *lockout.Book) *DeviceBus {
	return &DeviceBus{commands: make(map[string]model.DeviceCommand), lockouts: book}
}

func (b *DeviceBus) Publish(ctx context.Context, command model.DeviceCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if command.DeviceID == "" || command.Generation == 0 {
		return errors.New("device command is incomplete")
	}
	if b.lockouts != nil && b.lockouts.IsLocked(command.DeviceID) && !isSafetyAction(command.Action) {
		return errors.New("device command rejected: device is locked out for field maintenance")
	}
	b.mu.Lock()
	current, exists := b.commands[command.DeviceID]
	if exists && command.Generation < current.Generation {
		b.mu.Unlock()
		return errors.New("device command generation is stale")
	}
	b.commands[command.DeviceID] = command
	b.mu.Unlock()
	return nil
}

func (b *DeviceBus) All() []model.DeviceCommand {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]model.DeviceCommand, 0, len(b.commands))
	for _, command := range b.commands {
		result = append(result, command)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].IssuedAt.Before(result[j].IssuedAt) })
	return result
}

func (b *DeviceBus) StopLine(lineID model.LineID, generation uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, command := range b.commands {
		if command.LineID != lineID {
			continue
		}
		command.Generation = generation
		command.Action = "stop"
		command.Setpoint = 0
		b.commands[id] = command
	}
}

// StopDevice 用安全类指令覆盖该设备在途的控制命令（封锁挂牌时调用）。
// 旋转设备下发 stop，阀门下发 close。
func (b *DeviceBus) StopDevice(deviceID string, lineID model.LineID, generation uint64, action string, issuedAt time.Time) {
	if !isSafetyAction(action) {
		action = "stop"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	command, exists := b.commands[deviceID]
	if !exists {
		return
	}
	command.LineID = lineID
	command.Generation = generation
	command.Action = action
	command.Setpoint = 0
	command.IssuedAt = issuedAt.UTC()
	b.commands[deviceID] = command
}

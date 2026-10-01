package process

import (
	"context"
	"errors"

	"github.com/wyw14/cry-97/internal/journal"
	"github.com/wyw14/cry-97/internal/lockout"
	"github.com/wyw14/cry-97/internal/model"
)

func (p *Plant) RecoverLine(ctx context.Context, lineID model.LineID) (uint64, error) {
	if snapshot, found, err := p.snapshots.Load(lineID); err != nil {
		return 0, err
	} else if found {
		p.mu.Lock()
		p.lines[lineID] = snapshot
		p.mu.Unlock()
	}
	replayer, err := journal.NewReplayer(p.store, p)
	if err != nil {
		return 0, err
	}
	return replayer.ReplayLine(ctx, lineID)
}

func (p *Plant) ApplyRecovered(ctx context.Context, event model.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	state, ok := p.lines[event.LineID]
	if !ok {
		return errors.New("recovery line is not configured")
	}
	if event.Sequence <= state.LastEventSeq {
		return nil
	}
	if state.Emergency && event.Generation < state.Generation {
		state.LastEventSeq = event.Sequence
		p.lines[event.LineID] = state
		return nil
	}
	if event.IsTerminal() {
		var record EmergencyRecord
		if err := event.Decode(&record); err != nil {
			return err
		}
		if record.LineID != event.LineID || record.Generation != event.Generation {
			return errors.New("emergency recovery payload does not match event identity")
		}
		state = state.Isolate(event.Sequence, event.OccurredAt)
		p.lines[event.LineID] = state
		p.devices.StopLine(event.LineID, state.Generation)
		return nil
	}
	if event.Kind == model.EventDeviceCommanded {
		var command model.DeviceCommand
		if err := event.Decode(&command); err != nil {
			return err
		}
		if err := p.devices.Publish(ctx, command); err != nil {
			return err
		}
	}
	if event.Kind == model.EventDeviceLocked || event.Kind == model.EventLockoutUpdated || event.Kind == model.EventDeviceRestored {
		var tag lockout.Tag
		if err := event.Decode(&tag); err != nil {
			return err
		}
		if tag.LineID != event.LineID {
			return errors.New("lockout recovery payload does not match event line")
		}
		p.lockouts.ApplyTag(tag)
		switch event.Kind {
		case model.EventDeviceLocked:
			// 重启后仍在封锁期的设备，重新施加安全停机并挂起在途工序。
			p.quiesceLockedDevice(tag, event.Generation, event.OccurredAt)
			p.reassertLockoutAlarm(tag, event.OccurredAt)
		case model.EventDeviceRestored:
			p.resumeRestoredDevice(tag, event.OccurredAt)
			p.reassertLockoutAlarm(tag, event.OccurredAt)
		}
	}
	state.LastEventSeq = event.Sequence
	state.UpdatedAt = event.OccurredAt
	p.lines[event.LineID] = state
	return nil
}

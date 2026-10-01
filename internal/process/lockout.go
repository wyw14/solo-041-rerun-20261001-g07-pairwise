package process

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/lockout"
	"github.com/wyw14/cry-97/internal/model"
)

const alarmCodeLockout = "DEVICE_LOCKOUT"

// LockDevice 登记设备现场检修停用（挂牌上锁）：记录停用原因与责任人，
// 按安全规则停止或挂起该设备上的在途动作，并发出检修报警。
// 封锁期间该设备的一切非安全类控制命令都会被拒绝。
func (p *Plant) LockDevice(ctx context.Context, deviceID, reason, owner string) (lockout.Tag, error) {
	info, ok := p.deviceIndex.Lookup(deviceID)
	if !ok {
		return lockout.Tag{}, errors.New("lockout target device is not registered")
	}
	if p.lockouts.IsLocked(deviceID) {
		return lockout.Tag{}, errors.New("device is already locked out for maintenance")
	}
	now := p.now()
	tag, err := lockout.Build(deviceID, info.Kind, info.LineID, reason, owner, now)
	if err != nil {
		return lockout.Tag{}, err
	}
	p.mu.Lock()
	state := p.lines[info.LineID]
	notice := model.NewAlarm(
		info.LineID, alarmCodeLockout,
		fmt.Sprintf("device %s locked out for maintenance: %s", deviceID, tag.Reason),
		model.SeverityWarning, now,
	)
	tag.AlarmID = notice.ID
	event, err := model.NewEvent(info.LineID, state.Generation, model.EventDeviceLocked, tag, now)
	if err != nil {
		p.mu.Unlock()
		return lockout.Tag{}, err
	}
	persisted, err := p.store.Append(ctx, event)
	if err != nil {
		p.mu.Unlock()
		return lockout.Tag{}, err
	}
	state.LastEventSeq = persisted.Sequence
	state.UpdatedAt = now.UTC()
	p.lines[info.LineID] = state
	p.mu.Unlock()

	p.lockouts.ApplyTag(tag)
	p.quiesceLockedDevice(tag, state.Generation, now)
	p.alarms.Raise(notice)
	return tag, nil
}

// ConfirmLockoutCheck 由现场逐项确认挂牌安全检查项。
func (p *Plant) ConfirmLockoutCheck(ctx context.Context, tagID uuid.UUID, code, operator, note string) (lockout.Tag, error) {
	tag, err := p.activeTag(tagID)
	if err != nil {
		return lockout.Tag{}, err
	}
	now := p.now()
	updated, err := lockout.ApplyCheck(tag, code, operator, note, now)
	if err != nil {
		return lockout.Tag{}, err
	}
	if err := p.appendLockoutEvent(ctx, model.EventLockoutUpdated, updated, now); err != nil {
		return lockout.Tag{}, err
	}
	p.lockouts.ApplyTag(updated)
	return updated, nil
}

// SubmitLockoutPreCheck 复役前重新执行运行前校验；校验失败会留痕但不能复役。
func (p *Plant) SubmitLockoutPreCheck(ctx context.Context, tagID uuid.UUID, input lockout.PreCheckInput) (lockout.Tag, error) {
	tag, err := p.activeTag(tagID)
	if err != nil {
		return lockout.Tag{}, err
	}
	now := p.now()
	updated, err := lockout.ApplyPreCheck(tag, input, now)
	if err != nil {
		return lockout.Tag{}, err
	}
	if err := p.appendLockoutEvent(ctx, model.EventLockoutUpdated, updated, now); err != nil {
		return lockout.Tag{}, err
	}
	p.lockouts.ApplyTag(updated)
	return updated, nil
}

// RestoreDevice 复役：必须所有安全检查项已确认、运行前校验通过；
// 复役后解除命令封锁，恢复被挂起的在途工序，并消除检修报警。
func (p *Plant) RestoreDevice(ctx context.Context, tagID uuid.UUID, operator string) (lockout.Tag, error) {
	tag, err := p.activeTag(tagID)
	if err != nil {
		return lockout.Tag{}, err
	}
	now := p.now()
	updated, err := lockout.BuildRestore(tag, operator, now)
	if err != nil {
		return lockout.Tag{}, err
	}
	if err := p.appendLockoutEvent(ctx, model.EventDeviceRestored, updated, now); err != nil {
		return lockout.Tag{}, err
	}
	p.lockouts.ApplyTag(updated)
	p.resumeRestoredDevice(updated, now)
	if tag.AlarmID != uuid.Nil {
		p.alarms.RecoverIfExists(tag.AlarmID, now)
	}
	return updated, nil
}

func (p *Plant) appendLockoutEvent(ctx context.Context, kind model.EventKind, tag lockout.Tag, now time.Time) error {
	p.mu.Lock()
	state := p.lines[tag.LineID]
	event, err := model.NewEvent(tag.LineID, state.Generation, kind, tag, now)
	if err != nil {
		p.mu.Unlock()
		return err
	}
	persisted, err := p.store.Append(ctx, event)
	if err != nil {
		p.mu.Unlock()
		return err
	}
	state.LastEventSeq = persisted.Sequence
	state.UpdatedAt = now.UTC()
	p.lines[tag.LineID] = state
	p.mu.Unlock()
	return nil
}

func (p *Plant) activeTag(tagID uuid.UUID) (lockout.Tag, error) {
	tag, ok := p.lockouts.Get(tagID)
	if !ok {
		return lockout.Tag{}, errors.New("lockout tag is not found")
	}
	if !tag.Active() {
		return lockout.Tag{}, errors.New("lockout tag has already been restored")
	}
	return tag, nil
}

// quiesceLockedDevice 按设备类型执行安全规则：旋转设备停机，阀门关闭，
// 正在进行的泵工序/交接挂起而不是静默恢复，排水许可随阀闭吊销。
func (p *Plant) quiesceLockedDevice(tag lockout.Tag, generation uint64, now time.Time) {
	switch tag.Kind {
	case model.DeviceBlower:
		_, _, _ = p.aeration.ForceStop(tag.DeviceID, now)
		p.devices.StopDevice(tag.DeviceID, tag.LineID, generation, "stop", now)
	case model.DevicePump:
		if _, _, err := p.pumps.Suspend(tag.DeviceID, now); err == nil {
			_, _, _ = p.sequences.Suspend(tag.LineID, tag.DeviceID, now)
			_, _ = p.handovers.Suspend(tag.LineID, tag.DeviceID, now)
		}
		p.devices.StopDevice(tag.DeviceID, tag.LineID, generation, "stop", now)
	case model.DeviceValve:
		if lineID := p.valves.LineForDevice(tag.DeviceID); lineID != "" {
			if _, _, err := p.valves.ForceClose(lineID, now); err == nil {
				_, _ = p.permits.RevokeLine(lineID, now)
			}
		}
		p.devices.StopDevice(tag.DeviceID, tag.LineID, generation, "close", now)
	}
}

// resumeRestoredDevice 在设备复役后解除在途工序挂起；
// 被安全停止的旋转设备不会自动重启，须由控制环按校验后的状态重新驱动。
func (p *Plant) resumeRestoredDevice(tag lockout.Tag, now time.Time) {
	switch tag.Kind {
	case model.DevicePump:
		p.pumps.ClearSuspension(tag.DeviceID, now)
		_, _ = p.sequences.Resume(tag.LineID, now)
		_, _ = p.handovers.Resume(tag.LineID, now)
	case model.DeviceValve:
		if lineID := p.valves.LineForDevice(tag.DeviceID); lineID != "" {
			_, _, _ = p.permits.ReinstateLine(lineID, now)
		}
	}
}

// reassertLockoutAlarm 在 journal 重放后重建检修报警，保证重启后报警与封锁互相可见。
func (p *Plant) reassertLockoutAlarm(tag lockout.Tag, at time.Time) {
	if tag.AlarmID == uuid.Nil {
		return
	}
	if tag.Active() {
		notice := model.Alarm{
			ID: tag.AlarmID, LineID: tag.LineID, Code: alarmCodeLockout,
			Message:  fmt.Sprintf("device %s locked out for maintenance: %s", tag.DeviceID, tag.Reason),
			Severity: model.SeverityWarning, RaisedAt: tag.LockedAt,
		}
		p.alarms.Raise(notice)
		return
	}
	_, _ = p.alarms.RecoverIfExists(tag.AlarmID, at)
}

func (p *Plant) Lockouts() []lockout.Tag {
	return p.lockouts.All()
}

func (p *Plant) ActiveLockouts() []lockout.Tag {
	return p.lockouts.Active()
}

func (p *Plant) Devices() []DeviceInfo {
	return p.deviceIndex.All()
}

// LockoutCountsByLine 返回各处理线当前在效的封锁数量。
func (p *Plant) LockoutCountsByLine() map[model.LineID]int {
	return p.lockouts.ActiveCountByLine()
}

// ActiveLockoutCount 返回全厂在效封锁总数，供健康检查展示。
func (p *Plant) ActiveLockoutCount() int {
	return len(p.lockouts.Active())
}

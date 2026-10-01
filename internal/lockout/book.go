package lockout

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
)

// Phase 是现场检修封锁的生命周期阶段。
type Phase string

const (
	// PhaseLocked 已登记停用并挂牌，安全检查项待逐项确认。
	PhaseLocked Phase = "locked"
	// PhaseChecksConfirmed 安全检查项已全部确认，等待运行前校验。
	PhaseChecksConfirmed Phase = "checks_confirmed"
	// PhaseReady 运行前校验通过，等待复役。
	PhaseReady Phase = "ready"
	// PhaseRestored 已复役，设备恢复可用。
	PhaseRestored Phase = "restored"
)

// Tag 是一次设备现场检修封锁（挂牌）的完整登记记录。
type Tag struct {
	ID          uuid.UUID        `json:"id"`
	DeviceID    string           `json:"device_id"`
	Kind        model.DeviceKind `json:"kind"`
	LineID      model.LineID     `json:"line_id"`
	Reason      string           `json:"reason"`
	Owner       string           `json:"owner"`
	Phase       Phase            `json:"phase"`
	Checks      []SafetyCheck    `json:"checks"`
	PreCheck    *PreCheck        `json:"pre_check,omitempty"`
	AlarmID     uuid.UUID        `json:"alarm_id,omitempty"`
	LockedAt    time.Time        `json:"locked_at"`
	ConfirmedAt *time.Time       `json:"confirmed_at,omitempty"`
	RestoredAt  *time.Time       `json:"restored_at,omitempty"`
	RestoredBy  string           `json:"restored_by,omitempty"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

func (t Tag) Active() bool {
	return t.Phase != PhaseRestored
}

func (t Tag) checksConfirmed() bool {
	for _, check := range t.Checks {
		if !check.Confirmed {
			return false
		}
	}
	return len(t.Checks) > 0
}

// Book 保存所有设备的封锁记录，是设备控制层的联锁依据。
type Book struct {
	mu      sync.RWMutex
	tags    map[uuid.UUID]Tag
	devices map[string]uuid.UUID
}

func NewBook() *Book {
	return &Book{tags: make(map[uuid.UUID]Tag), devices: make(map[string]uuid.UUID)}
}

// Build 构造一次新的封锁登记记录，但不写入状态簿，
// 便于调用方先持久化事件再提交内存状态。
func Build(deviceID string, kind model.DeviceKind, lineID model.LineID, reason, owner string, now time.Time) (Tag, error) {
	if deviceID == "" || lineID == "" {
		return Tag{}, errors.New("lockout device identity is incomplete")
	}
	if strings.TrimSpace(reason) == "" {
		return Tag{}, errors.New("lockout reason is required")
	}
	if strings.TrimSpace(owner) == "" {
		return Tag{}, errors.New("lockout responsible owner is required")
	}
	stamp := now.UTC()
	return Tag{
		ID:        uuid.New(),
		DeviceID:  deviceID,
		Kind:      kind,
		LineID:    lineID,
		Reason:    strings.TrimSpace(reason),
		Owner:     strings.TrimSpace(owner),
		Phase:     PhaseLocked,
		Checks:    safetyCheckTemplates(kind),
		LockedAt:  stamp,
		UpdatedAt: stamp,
	}, nil
}

// Place 登记设备停用并挂牌，初始化该类设备的安全检查项。
func (b *Book) Place(deviceID string, kind model.DeviceKind, lineID model.LineID, reason, owner string, now time.Time) (Tag, error) {
	tag, err := Build(deviceID, kind, lineID, reason, owner, now)
	if err != nil {
		return Tag{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.devices[deviceID]; exists {
		return Tag{}, errors.New("device is already locked out for maintenance")
	}
	b.tags[tag.ID] = tag
	b.devices[deviceID] = tag.ID
	return tag, nil
}

// ConfirmCheck 逐项确认安全检查；全部确认后进入 checks_confirmed 阶段。
func (b *Book) ConfirmCheck(tagID uuid.UUID, code, operator, note string, now time.Time) (Tag, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tag, ok := b.mutable(tagID)
	if !ok {
		return Tag{}, errors.New("active lockout tag is not found")
	}
	updated, err := ApplyCheck(tag, code, operator, note, now)
	if err != nil {
		return Tag{}, err
	}
	b.tags[tagID] = updated
	return updated, nil
}

// ApplyCheck 在给定封锁记录上确认一项安全检查（纯函数，供事件持久化路径使用）。
func ApplyCheck(tag Tag, code, operator, note string, now time.Time) (Tag, error) {
	if !tag.Active() {
		return Tag{}, errors.New("active lockout tag is not found")
	}
	if code == "" || strings.TrimSpace(operator) == "" {
		return Tag{}, errors.New("safety check code and confirming operator are required")
	}
	matched := false
	for index := range tag.Checks {
		if tag.Checks[index].Code != code {
			continue
		}
		if tag.Checks[index].Confirmed {
			return Tag{}, errors.New("safety check is already confirmed")
		}
		stamp := now.UTC()
		tag.Checks[index].Confirmed = true
		tag.Checks[index].ConfirmedBy = strings.TrimSpace(operator)
		tag.Checks[index].Note = strings.TrimSpace(note)
		tag.Checks[index].ConfirmedAt = &stamp
		matched = true
	}
	if !matched {
		return Tag{}, errors.New("safety check item is not part of the lockout")
	}
	if tag.checksConfirmed() {
		stamp := now.UTC()
		tag.Phase = PhaseChecksConfirmed
		tag.ConfirmedAt = &stamp
	}
	tag.UpdatedAt = now.UTC()
	return tag, nil
}

// RecordPreCheck 重新执行并记录运行前校验；只有全部安全检查确认后才允许执行，
// 校验失败也会留痕，但不会推进到 ready 阶段。
func (b *Book) RecordPreCheck(tagID uuid.UUID, input PreCheckInput, now time.Time) (Tag, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tag, ok := b.mutable(tagID)
	if !ok {
		return Tag{}, errors.New("active lockout tag is not found")
	}
	updated, err := ApplyPreCheck(tag, input, now)
	if err != nil {
		return Tag{}, err
	}
	b.tags[tagID] = updated
	return updated, nil
}

// ApplyPreCheck 在给定封锁记录上记录一次运行前校验（纯函数）。
func ApplyPreCheck(tag Tag, input PreCheckInput, now time.Time) (Tag, error) {
	if !tag.Active() {
		return Tag{}, errors.New("active lockout tag is not found")
	}
	if !tag.checksConfirmed() {
		return Tag{}, errors.New("all safety checks must be confirmed before the pre-start check")
	}
	result := input.evaluate(now)
	tag.PreCheck = &result
	tag.UpdatedAt = now.UTC()
	if result.Passed {
		tag.Phase = PhaseReady
	}
	return tag, nil
}

// Restore 复役：必须安全检查全部确认且运行前校验通过。
func (b *Book) Restore(tagID uuid.UUID, operator string, now time.Time) (Tag, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tag, ok := b.mutable(tagID)
	if !ok {
		return Tag{}, errors.New("active lockout tag is not found")
	}
	updated, err := BuildRestore(tag, operator, now)
	if err != nil {
		return Tag{}, err
	}
	delete(b.devices, tag.DeviceID)
	b.tags[tagID] = updated
	return updated, nil
}

// BuildRestore 在给定封锁记录上完成复役转换（纯函数），不改变设备可用性映射。
func BuildRestore(tag Tag, operator string, now time.Time) (Tag, error) {
	if !tag.Active() {
		return Tag{}, errors.New("active lockout tag is not found")
	}
	if strings.TrimSpace(operator) == "" {
		return Tag{}, errors.New("restore operator is required")
	}
	if !tag.checksConfirmed() {
		return Tag{}, errors.New("lockout cannot be restored before safety checks are confirmed")
	}
	if tag.PreCheck == nil || !tag.PreCheck.Passed {
		return Tag{}, errors.New("lockout cannot be restored before a passing pre-start check")
	}
	stamp := now.UTC()
	tag.Phase = PhaseRestored
	tag.RestoredAt = &stamp
	tag.RestoredBy = strings.TrimSpace(operator)
	tag.UpdatedAt = stamp
	return tag, nil
}

// ApplyTag 按 journal 重放的封锁记录对齐内存状态（幂等）。
func (b *Book) ApplyTag(tag Tag) {
	if tag.ID == uuid.Nil || tag.DeviceID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tags[tag.ID] = tag
	if tag.Active() {
		b.devices[tag.DeviceID] = tag.ID
	} else {
		if current, ok := b.devices[tag.DeviceID]; ok && current == tag.ID {
			delete(b.devices, tag.DeviceID)
		}
	}
}

func (b *Book) mutable(tagID uuid.UUID) (Tag, bool) {
	tag, ok := b.tags[tagID]
	if !ok || !tag.Active() {
		return Tag{}, false
	}
	return tag, true
}

func (b *Book) IsLocked(deviceID string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.devices[deviceID]
	return ok
}

func (b *Book) ForDevice(deviceID string) (Tag, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	tagID, ok := b.devices[deviceID]
	if !ok {
		return Tag{}, false
	}
	return b.tags[tagID], true
}

func (b *Book) Get(tagID uuid.UUID) (Tag, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	tag, ok := b.tags[tagID]
	return tag, ok
}

func (b *Book) Active() []Tag {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]Tag, 0, len(b.devices))
	for _, id := range b.devices {
		result = append(result, b.tags[id])
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LockedAt.Before(result[j].LockedAt) })
	return result
}

func (b *Book) All() []Tag {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]Tag, 0, len(b.tags))
	for _, tag := range b.tags {
		result = append(result, tag)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Active() != result[j].Active() {
			return result[i].Active()
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result
}

func (b *Book) ActiveForLine(lineID model.LineID) []Tag {
	active := b.Active()
	result := make([]Tag, 0, len(active))
	for _, tag := range active {
		if tag.LineID == lineID {
			result = append(result, tag)
		}
	}
	return result
}

func (b *Book) ActiveCountByLine() map[model.LineID]int {
	counts := make(map[model.LineID]int)
	for _, tag := range b.Active() {
		counts[tag.LineID]++
	}
	return counts
}

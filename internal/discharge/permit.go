package discharge

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
)

type Permit struct {
	ID             uuid.UUID    `json:"id"`
	LineID         model.LineID `json:"line_id"`
	BatchID        uuid.UUID    `json:"batch_id"`
	SampleID       uuid.UUID    `json:"sample_id"`
	SampleRevision uint64       `json:"sample_revision"`
	Active         bool         `json:"active"`
	IssuedAt       time.Time    `json:"issued_at"`
	RevokedAt      *time.Time   `json:"revoked_at,omitempty"`
}

type PermitBook struct {
	mu      sync.RWMutex
	byBatch map[uuid.UUID]Permit
	valves  *ValveController
}

func NewPermitBook(valves *ValveController) (*PermitBook, error) {
	if valves == nil {
		return nil, errors.New("permit book requires valve controller")
	}
	return &PermitBook{byBatch: make(map[uuid.UUID]Permit), valves: valves}, nil
}

func (b *PermitBook) ApplyResult(result model.LabResult, now time.Time) (Permit, error) {
	if !result.Valid || result.SampleID == uuid.Nil || result.BatchID == uuid.Nil || result.Revision == 0 {
		return Permit{}, errors.New("permit result is invalid")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	current, exists := b.byBatch[result.BatchID]
	if exists && current.SampleID == result.SampleID && result.Revision <= current.SampleRevision {
		return current, errors.New("permit result revision is stale")
	}
	if !result.Qualified() {
		if exists && current.Active {
			stamp := now.UTC()
			current.Active = false
			current.SampleRevision = result.Revision
			current.RevokedAt = &stamp
			b.byBatch[result.BatchID] = current
			if err := b.valves.Hold(result.LineID, now); err != nil {
				return Permit{}, err
			}
			return current, nil
		}
		return Permit{}, errors.New("result is not qualified for discharge")
	}
	permit := Permit{
		ID: uuid.New(), LineID: result.LineID, BatchID: result.BatchID,
		SampleID: result.SampleID, SampleRevision: result.Revision, Active: true, IssuedAt: now.UTC(),
	}
	if err := b.valves.Open(result.LineID, permit.ID, now); err != nil {
		return Permit{}, err
	}
	b.byBatch[result.BatchID] = permit
	return permit, nil
}

func (b *PermitBook) Current(batchID uuid.UUID) (Permit, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	permit, ok := b.byBatch[batchID]
	return permit, ok
}

// latestForLine 必须在持锁状态下调用，返回该处理线最近一张许可。
func (b *PermitBook) latestForLine(lineID model.LineID) (Permit, bool) {
	var found Permit
	matched := false
	for _, permit := range b.byBatch {
		if permit.LineID != lineID {
			continue
		}
		if !matched || permit.IssuedAt.After(found.IssuedAt) {
			found, matched = permit, true
		}
	}
	return found, matched
}

// RevokeLine 因排水阀封锁挂牌而吊销该处理线在效的排放许可，不重新开阀。
func (b *PermitBook) RevokeLine(lineID model.LineID, now time.Time) (Permit, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	permit, ok := b.latestForLine(lineID)
	if !ok || !permit.Active {
		return Permit{}, false
	}
	stamp := now.UTC()
	permit.Active = false
	permit.RevokedAt = &stamp
	b.byBatch[permit.BatchID] = permit
	return permit, true
}

// ReinstateLine 在排水阀复役后，依据最近一次合格化验重新签发排放许可并开阀。
// 该处理线从未取得过许可时返回 false，由调用方按常规流程重新化验申请。
func (b *PermitBook) ReinstateLine(lineID model.LineID, now time.Time) (Permit, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	previous, ok := b.latestForLine(lineID)
	if !ok || previous.SampleID == uuid.Nil {
		return Permit{}, false, nil
	}
	permit := Permit{
		ID: uuid.New(), LineID: lineID, BatchID: previous.BatchID,
		SampleID: previous.SampleID, SampleRevision: previous.SampleRevision,
		Active: true, IssuedAt: now.UTC(),
	}
	if err := b.valves.Open(lineID, permit.ID, now); err != nil {
		return Permit{}, false, err
	}
	b.byBatch[permit.BatchID] = permit
	return permit, true, nil
}

func (b *PermitBook) All() []Permit {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]Permit, 0, len(b.byBatch))
	for _, permit := range b.byBatch {
		result = append(result, permit)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].IssuedAt.After(result[j].IssuedAt) })
	return result
}

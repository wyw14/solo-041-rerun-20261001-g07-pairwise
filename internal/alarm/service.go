package alarm

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
)

type Service struct {
	mu     sync.RWMutex
	alarms map[uuid.UUID]model.Alarm
}

func NewService() *Service {
	return &Service{alarms: make(map[uuid.UUID]model.Alarm)}
}

func (s *Service) Raise(alarm model.Alarm) model.Alarm {
	s.mu.Lock()
	s.alarms[alarm.ID] = alarm
	s.mu.Unlock()
	return alarm
}

func (s *Service) Acknowledge(id uuid.UUID) (model.Alarm, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.alarms[id]
	if !ok {
		return model.Alarm{}, errors.New("alarm is not found")
	}
	value = value.Acknowledge()
	s.alarms[id] = value
	return value, nil
}

func (s *Service) Recover(id uuid.UUID, now time.Time) (model.Alarm, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.alarms[id]
	if !ok {
		return model.Alarm{}, errors.New("alarm is not found")
	}
	value = value.Recover(now)
	s.alarms[id] = value
	return value, nil
}

// RecoverIfExists 在报警存在时消除它，不存在时不报错（供重放与复役幂等调用）。
func (s *Service) RecoverIfExists(id uuid.UUID, now time.Time) (model.Alarm, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.alarms[id]
	if !ok {
		return model.Alarm{}, false
	}
	value = value.Recover(now)
	s.alarms[id] = value
	return value, true
}

func (s *Service) Active(lineID model.LineID) []model.Alarm {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]model.Alarm, 0)
	for _, value := range s.alarms {
		if value.LineID == lineID && value.Active() {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RaisedAt.Before(result[j].RaisedAt) })
	return result
}

func (s *Service) All() []model.Alarm {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]model.Alarm, 0, len(s.alarms))
	for _, value := range s.alarms {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RaisedAt.After(result[j].RaisedAt) })
	return result
}

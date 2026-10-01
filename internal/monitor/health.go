package monitor

import (
	"time"

	"github.com/wyw14/cry-97/internal/model"
)

type HealthSource interface {
	Healthy() bool
	Lines() []model.LineState
	Alarms() []model.Alarm
	ActiveLockoutCount() int
}

type Health struct {
	Ready          bool      `json:"ready"`
	ProcessLines   int       `json:"process_lines"`
	ActiveAlarms   int       `json:"active_alarms"`
	ActiveLockouts int       `json:"active_lockouts"`
	ObservedAt     time.Time `json:"observed_at"`
}

type Service struct {
	source HealthSource
	now    func() time.Time
}

func NewService(source HealthSource, now func() time.Time) *Service {
	return &Service{source: source, now: now}
}

func (s *Service) Observe() Health {
	alarms := s.source.Alarms()
	active := 0
	for _, alarm := range alarms {
		if alarm.Active() {
			active++
		}
	}
	value := Health{
		Ready: s.source.Healthy(), ProcessLines: len(s.source.Lines()),
		ActiveAlarms: active, ActiveLockouts: s.source.ActiveLockoutCount(),
		ObservedAt: s.now().UTC(),
	}
	return value
}

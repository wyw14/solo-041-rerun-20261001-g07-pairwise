package lockout

import (
	"testing"
	"time"

	"github.com/wyw14/cry-97/internal/model"
)

func testClock() time.Time {
	return time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
}

func TestPlaceRequiresIdentityAndResponsibleOwner(t *testing.T) {
	book := NewBook()
	now := testClock()
	cases := []struct {
		name             string
		deviceID, reason string
		owner            string
	}{
		{"missing device", "", "repair", "owner-1"},
		{"missing reason", "pump-1", "", "owner-1"},
		{"missing owner", "pump-1", "repair", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := book.Place(tc.deviceID, model.DevicePump, "line-1", tc.reason, tc.owner, now); err == nil {
				t.Fatal("expected lockout placement to be rejected")
			}
		})
	}
}

func TestLockoutLifecycleAndRestoreGate(t *testing.T) {
	book := NewBook()
	now := testClock()
	tag, err := book.Place("pump-line-1-A", model.DevicePump, "line-1", "seal replacement", "fitter-zhou", now)
	if err != nil {
		t.Fatalf("place lockout: %v", err)
	}
	if tag.Phase != PhaseLocked || len(tag.Checks) == 0 {
		t.Fatalf("new tag should be locked with safety checks, got %+v", tag)
	}
	if !book.IsLocked("pump-line-1-A") {
		t.Fatal("device should report locked after placing tag")
	}
	if _, err := book.Place("pump-line-1-A", model.DevicePump, "line-1", "again", "fitter-zhou", now); err == nil {
		t.Fatal("duplicate lockout must be rejected")
	}

	// 未完成全部安全检查前，运行前校验与复役都必须被拒绝。
	if _, err := book.RecordPreCheck(tag.ID, PreCheckInput{Insulation: 1, Vibration: 3, Temperature: 40, Operator: "eng-1"}, now); err == nil {
		t.Fatal("pre-start check must wait for all safety checks")
	}
	if _, err := book.Restore(tag.ID, "supervisor-1", now); err == nil {
		t.Fatal("restore before safety checks must be rejected")
	}

	// 逐项确认安全检查。
	for index, check := range tag.Checks {
		tag, err = book.ConfirmCheck(tag.ID, check.Code, "safety-li", "", now)
		if err != nil {
			t.Fatalf("confirm check %s: %v", check.Code, err)
		}
		if index < len(tag.Checks)-1 && tag.Phase != PhaseLocked {
			t.Fatalf("phase should stay locked until final check, got %s", tag.Phase)
		}
		if _, err := book.ConfirmCheck(tag.ID, check.Code, "safety-li", "", now); err == nil {
			t.Fatal("re-confirming the same check must be rejected")
		}
	}
	if tag.Phase != PhaseChecksConfirmed || tag.ConfirmedAt == nil {
		t.Fatalf("all checks should move tag to checks_confirmed, got %s", tag.Phase)
	}

	// 运行前校验失败：留痕但不得进入 ready，也不得复役。
	failed, err := book.RecordPreCheck(tag.ID, PreCheckInput{Insulation: 0.1, Vibration: 3, Temperature: 40, Operator: "eng-1"}, now)
	if err != nil {
		t.Fatalf("failing precheck is recorded, not rejected: %v", err)
	}
	if failed.PreCheck == nil || failed.PreCheck.Passed || failed.Phase != PhaseChecksConfirmed {
		t.Fatalf("failed precheck must not advance phase: %+v", failed.PreCheck)
	}
	if _, err := book.Restore(tag.ID, "supervisor-1", now); err == nil {
		t.Fatal("restore after failed precheck must be rejected")
	}

	// 重新执行并通过运行前校验。
	tag, err = book.RecordPreCheck(tag.ID, PreCheckInput{Insulation: 1.2, Vibration: 3.4, Temperature: 46, Operator: "eng-1"}, now)
	if err != nil {
		t.Fatalf("passing precheck: %v", err)
	}
	if tag.Phase != PhaseReady || tag.PreCheck == nil || !tag.PreCheck.Passed {
		t.Fatalf("passing precheck should move tag to ready, got %s", tag.Phase)
	}

	// 复役必须有确认人。
	if _, err := book.Restore(tag.ID, "  ", now); err == nil {
		t.Fatal("restore without operator must be rejected")
	}
	restored, err := book.Restore(tag.ID, "supervisor-1", now)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Phase != PhaseRestored || restored.RestoredAt == nil || restored.Active() {
		t.Fatalf("tag should be restored, got %+v", restored)
	}
	if book.IsLocked("pump-line-1-A") {
		t.Fatal("device must be unlocked after restore")
	}
}

func TestApplyTagReplayIsIdempotent(t *testing.T) {
	book := NewBook()
	now := testClock()
	tag, err := Build("blower-basin-line-1", model.DeviceBlower, "line-1", "vibration survey", "fitter-wang", now)
	if err != nil {
		t.Fatalf("build tag: %v", err)
	}
	book.ApplyTag(tag)
	book.ApplyTag(tag) // 重放重复事件不得产生重复映射。
	if !book.IsLocked(tag.DeviceID) {
		t.Fatal("replayed active tag should lock the device")
	}
	active := book.Active()
	if len(active) != 1 {
		t.Fatalf("expected one active tag, got %d", len(active))
	}

	// 走完安全检查与运行前校验后复役，再重放复役记录。
	for _, check := range tag.Checks {
		var err error
		tag, err = ApplyCheck(tag, check.Code, "safety-li", "", now)
		if err != nil {
			t.Fatalf("apply check %s: %v", check.Code, err)
		}
	}
	tag, err = ApplyPreCheck(tag, PreCheckInput{Insulation: 1, Vibration: 3, Temperature: 40, Operator: "eng-1"}, now)
	if err != nil {
		t.Fatalf("apply precheck: %v", err)
	}
	restored, err := BuildRestore(tag, "supervisor-1", now)
	if err != nil {
		t.Fatalf("build restore: %v", err)
	}
	book.ApplyTag(restored)
	if book.IsLocked(tag.DeviceID) {
		t.Fatal("replayed restore must release the device")
	}
	if got := len(book.Active()); got != 0 {
		t.Fatalf("expected no active tags after replay, got %d", got)
	}
}

func TestPreCheckValidationBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		input PreCheckInput
		valid bool
	}{
		{"insulation low", PreCheckInput{Insulation: 0.4, Vibration: 3, Temperature: 40, Operator: "eng-1"}, false},
		{"vibration high", PreCheckInput{Insulation: 1, Vibration: 7.2, Temperature: 40, Operator: "eng-1"}, false},
		{"temperature high", PreCheckInput{Insulation: 1, Vibration: 3, Temperature: 86, Operator: "eng-1"}, false},
		{"missing operator", PreCheckInput{Insulation: 1, Vibration: 3, Temperature: 40}, false},
		{"within limits", PreCheckInput{Insulation: 0.5, Vibration: 7.1, Temperature: 85, Operator: "eng-1"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.input.validate()
			if tc.valid && err != nil {
				t.Fatalf("expected valid input, got %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatal("expected input to fail validation")
			}
		})
	}
}

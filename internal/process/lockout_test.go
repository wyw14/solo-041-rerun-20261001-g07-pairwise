package process

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/aeration"
	"github.com/wyw14/cry-97/internal/discharge"
	"github.com/wyw14/cry-97/internal/lockout"
	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/sludge"
)

type testRig struct {
	plant   *Plant
	dataDir string
	line    model.LineID
}

func newTestRig(t *testing.T) testRig {
	t.Helper()
	line := model.LineID("line-1")
	lines := []model.LineID{line, "line-2", "line-3"}
	dataDir := t.TempDir()
	clock := func() time.Time { return time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC) }
	plant, err := NewPlant(dataDir, lines, clock)
	if err != nil {
		t.Fatalf("new plant: %v", err)
	}
	return testRig{plant: plant, dataDir: dataDir, line: line}
}

func (r testRig) allLines() []model.LineID {
	return []model.LineID{r.line, "line-2", "line-3"}
}

func hasActiveLockoutAlarm(plant *Plant, line model.LineID) bool {
	for _, a := range plant.alarms.Active(line) {
		if a.Code == "DEVICE_LOCKOUT" {
			return true
		}
	}
	return false
}

func findBlower(plant *Plant, id string) aeration.Blower {
	for _, b := range plant.StatusBlowers() {
		if b.ID == id {
			return b
		}
	}
	return aeration.Blower{}
}

func findPump(plant *Plant, id string) sludge.Pump {
	for _, p := range plant.Pumps() {
		if p.ID == id {
			return p
		}
	}
	return sludge.Pump{}
}

func findValve(plant *Plant, line model.LineID) discharge.Valve {
	for _, v := range plant.valves.Snapshot() {
		if v.LineID == line {
			return v
		}
	}
	return discharge.Valve{}
}

// completeLockout 完成全部安全检查、通过运行前校验并复役。
func completeLockout(t *testing.T, ctx context.Context, plant *Plant, tag lockout.Tag) lockout.Tag {
	t.Helper()
	for _, check := range tag.Checks {
		var err error
		tag, err = plant.ConfirmLockoutCheck(ctx, tag.ID, check.Code, "safety-officer-1", "")
		if err != nil {
			t.Fatalf("confirm check %s: %v", check.Code, err)
		}
	}
	var err error
	tag, err = plant.SubmitLockoutPreCheck(ctx, tag.ID, lockout.PreCheckInput{
		Insulation: 1.2, Vibration: 3.4, Temperature: 46, Operator: "duty-engineer-1",
	})
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	tag, err = plant.RestoreDevice(ctx, tag.ID, "shift-supervisor-1")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	return tag
}

func TestBlowerLockoutStopsAndRejectsCommands(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)
	plant, line := rig.plant, rig.line

	// 提交稳定溶氧窗口，驱动风机运行。
	payload, _ := json.Marshal([]float64{2.0, 2.1, 2.0, 2.1})
	if _, command, err := plant.SubmitAeration(ctx, line, "basin-line-1", payload); err != nil || command == nil {
		t.Fatalf("drive blower: command=%v err=%v", command, err)
	}
	if blower := findBlower(plant, "blower-basin-line-1"); !blower.Running {
		t.Fatal("blower should be running before lockout")
	}

	tag, err := plant.LockDevice(ctx, "blower-basin-line-1", "bearing inspection", "fitter-zhou")
	if err != nil {
		t.Fatalf("lock blower: %v", err)
	}

	// 安全规则：风机强制停机；检修报警可见。
	blower := findBlower(plant, "blower-basin-line-1")
	if blower.Running || blower.Frequency != 0 || !blower.Locked {
		t.Fatalf("blower must be forced stopped and locked: %+v", blower)
	}
	if !hasActiveLockoutAlarm(plant, line) {
		t.Fatal("a DEVICE_LOCKOUT alarm must be visible while locked")
	}

	// 非安全命令在设备总线和曝气入口都被拒绝。
	busCmd, _ := model.NewDeviceCommand(line, uuid.New(), "blower-basin-line-1", model.DeviceBlower, 2, "set", plant.now())
	busCmd.Setpoint = 40
	if err := plant.devices.Publish(ctx, busCmd); err == nil {
		t.Fatal("non-safety command must be rejected during lockout")
	}
	if _, _, err := plant.SubmitAeration(ctx, line, "basin-line-1", payload); err == nil {
		t.Fatal("aeration control must be rejected while blower locked")
	}
	// 安全类停机命令仍然允许。
	stopCmd, _ := model.NewDeviceCommand(line, uuid.New(), "blower-basin-line-1", model.DeviceBlower, 3, "stop", plant.now())
	if err := plant.devices.Publish(ctx, stopCmd); err != nil {
		t.Fatalf("safety stop command must remain allowed: %v", err)
	}

	// 未完成门禁不得复役。
	if _, err := plant.RestoreDevice(ctx, tag.ID, "shift-supervisor-1"); err == nil {
		t.Fatal("restore must be blocked before checks and precheck")
	}

	restored := completeLockout(t, ctx, plant, tag)
	if restored.Phase != lockout.PhaseRestored {
		t.Fatalf("expected restored phase, got %s", restored.Phase)
	}
	if hasActiveLockoutAlarm(plant, line) {
		t.Fatal("lockout alarm must recover after restoration")
	}
	blower = findBlower(plant, "blower-basin-line-1")
	if blower.Locked || blower.Running {
		t.Fatalf("blower unlocked but must not auto-restart: %+v", blower)
	}
	// 复役后控制恢复，可重新驱动风机。
	if _, _, err := plant.SubmitAeration(ctx, line, "basin-line-1", payload); err != nil {
		t.Fatalf("aeration control should resume after restore: %v", err)
	}
}

func TestPumpLockoutSuspendsInFlightSequence(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)
	plant, line := rig.plant, rig.line
	pumpID := "pump-line-1-A"

	sequence, err := plant.StartSludgeSequence(line, pumpID, 1)
	if err != nil {
		t.Fatalf("start sequence: %v", err)
	}
	if _, err := plant.ConfirmSludgeFlow(line, sequence.ID, sequence.Generation, 120); err != nil {
		t.Fatalf("confirm flow: %v", err)
	}
	if pump := findPump(plant, pumpID); !pump.Running {
		t.Fatal("pump should be running before lockout")
	}

	tag, err := plant.LockDevice(ctx, pumpID, "seal replacement", "fitter-wang")
	if err != nil {
		t.Fatalf("lock pump: %v", err)
	}

	// 在途工序挂起、泵停止但保留属主；后续确认与新工序/交接被拒。
	pump := findPump(plant, pumpID)
	if pump.Running || !pump.Suspended || !pump.Locked {
		t.Fatalf("pump must stop and be suspended: %+v", pump)
	}
	current, _ := plant.sequences.Current(line)
	if current.Status != sludge.SequenceSuspended {
		t.Fatalf("in-flight sequence must be suspended, got %s", current.Status)
	}
	if _, err := plant.ConfirmSludgeFlow(line, sequence.ID, sequence.Generation, 120); err == nil {
		t.Fatal("flow confirmation must be rejected while suspended")
	}
	if _, err := plant.StartSludgeSequence(line, pumpID, 2); err == nil {
		t.Fatal("new sequence on a locked pump must be rejected")
	}
	if _, err := plant.StartHandover(line, "pump-line-1-B", pumpID); err == nil {
		t.Fatal("handover involving a locked pump must be rejected")
	}

	completeLockout(t, ctx, plant, tag)

	// 复役后工序解除挂起回到切换阶段，现场重新确认流量即可继续。
	current, _ = plant.sequences.Current(line)
	if current.Status != sludge.SequenceSwitching {
		t.Fatalf("sequence should resume to switching, got %s", current.Status)
	}
	pump = findPump(plant, pumpID)
	if pump.Suspended || pump.Locked || pump.Running {
		t.Fatalf("pump suspension cleared and stays stopped: %+v", pump)
	}
	if _, err := plant.ConfirmSludgeFlow(line, sequence.ID, sequence.Generation, 118); err != nil {
		t.Fatalf("flow confirmation should succeed after resume: %v", err)
	}
}

func TestValveLockoutClosesAndRevokesPermit(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)
	plant, line := rig.plant, rig.line

	// 建立批次并以合格化验取得排放许可，阀门开启。
	batch, err := plant.StartBatch(ctx, line, 760, "north-intake")
	if err != nil {
		t.Fatalf("start batch: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{
		"sample_id": uuid.New().String(), "line_id": string(line),
		"batch_id": batch.ID.String(), "revision": 1, "cod": 30, "ammonia": 2,
	})
	outcome, err := plant.SubmitLabPayload(ctx, payload)
	if err != nil || outcome.Permit == nil {
		t.Fatalf("qualify batch: outcome=%+v err=%v", outcome, err)
	}
	if valve := findValve(plant, line); !valve.Open {
		t.Fatal("discharge valve should be open with an active permit")
	}

	tag, err := plant.LockDevice(ctx, "valve-discharge-line-1", "actuator service", "fitter-li")
	if err != nil {
		t.Fatalf("lock valve: %v", err)
	}
	valve := findValve(plant, line)
	if valve.Open || !valve.Locked {
		t.Fatalf("valve must be forced closed and locked: %+v", valve)
	}
	if permit, _ := plant.permits.Current(batch.ID); permit.Active {
		t.Fatal("discharge permit must be revoked while the valve is locked")
	}
	status, err := plant.Status(line)
	if err != nil {
		t.Fatalf("line status: %v", err)
	}
	if status.LockoutCount != 1 {
		t.Fatalf("line status must expose the active lockout, got %d", status.LockoutCount)
	}

	completeLockout(t, ctx, plant, tag)
	valve = findValve(plant, line)
	if !valve.Open || valve.Locked {
		t.Fatalf("valve should reopen on restore: %+v", valve)
	}
	var activePermit bool
	for _, p := range plant.Permits() {
		if p.LineID == line && p.Active {
			activePermit = true
		}
	}
	if !activePermit {
		t.Fatal("a discharge permit must be reinstated after valve restore")
	}
}

func TestLockoutSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)
	plant, line, dataDir := rig.plant, rig.line, rig.dataDir

	// 建立在途工序并封锁泵，不做复役。
	sequence, err := plant.StartSludgeSequence(line, "pump-line-1-A", 1)
	if err != nil {
		t.Fatalf("start sequence: %v", err)
	}
	if _, err := plant.ConfirmSludgeFlow(line, sequence.ID, sequence.Generation, 120); err != nil {
		t.Fatalf("confirm flow: %v", err)
	}
	tag, err := plant.LockDevice(ctx, "pump-line-1-A", "seal replacement", "fitter-wang")
	if err != nil {
		t.Fatalf("lock pump: %v", err)
	}

	// 重启：同一数据目录重建 Plant 并重放日志。
	restarted, err := NewPlant(dataDir, rig.allLines(), plant.now)
	if err != nil {
		t.Fatalf("restart plant: %v", err)
	}
	for _, id := range rig.allLines() {
		if _, err := restarted.RecoverLine(ctx, id); err != nil {
			t.Fatalf("recover %s: %v", id, err)
		}
	}
	if !restarted.lockouts.IsLocked("pump-line-1-A") {
		t.Fatal("lockout must remain in effect after restart")
	}
	if !hasActiveLockoutAlarm(restarted, line) {
		t.Fatal("lockout alarm must be re-asserted after restart")
	}
	if _, err := restarted.StartSludgeSequence(line, "pump-line-1-A", 3); err == nil {
		t.Fatal("locked pump must still reject commands after restart")
	}
	if pump := findPump(restarted, "pump-line-1-A"); !pump.Locked || pump.Running {
		t.Fatalf("pump must remain locked and stopped after restart: %+v", pump)
	}
	if counts := restarted.LockoutCountsByLine(); counts[line] != 1 {
		t.Fatalf("line lockout count must survive restart, got %d", counts[line])
	}

	// 在重启后的实例上完成复役，然后再次重启，封锁应已解除。
	recoveredTag, ok := restarted.lockouts.Get(tag.ID)
	if !ok {
		t.Fatal("lockout tag must be recoverable by id")
	}
	completeLockout(t, ctx, restarted, recoveredTag)

	second, err := NewPlant(dataDir, rig.allLines(), plant.now)
	if err != nil {
		t.Fatalf("second restart: %v", err)
	}
	for _, id := range rig.allLines() {
		if _, err := second.RecoverLine(ctx, id); err != nil {
			t.Fatalf("second recover %s: %v", id, err)
		}
	}
	if second.lockouts.IsLocked("pump-line-1-A") {
		t.Fatal("restored device must stay unlocked after another restart")
	}
}

func TestLockUnknownDeviceRejected(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)
	if _, err := rig.plant.LockDevice(ctx, "pump-line-9-Z", "repair", "fitter-zhou"); err == nil {
		t.Fatal("locking an unregistered device must be rejected")
	}
}

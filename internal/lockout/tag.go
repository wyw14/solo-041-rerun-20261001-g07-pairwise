package lockout

import (
	"errors"
	"strings"
	"time"

	"github.com/wyw14/cry-97/internal/model"
)

// 运行前校验阈值：绝缘电阻、振动速度、绕组温度。
const (
	MinInsulationMOhm = 0.5
	MaxVibrationMMS   = 7.1
	MaxTemperatureC   = 85.0
)

// safetyActions 是封锁期间仍然允许下发的安全类指令（停机、关阀、保位）。
var safetyActions = map[string]bool{"stop": true, "close": true, "hold": true}

// IsSafetyAction 判断指令是否为封锁期间允许的安全类指令。
func IsSafetyAction(action string) bool {
	return safetyActions[action]
}

// SafetyCheck 是挂牌时必须逐项确认的现场安全检查项。
type SafetyCheck struct {
	Code        string     `json:"code"`
	Label       string     `json:"label"`
	Confirmed   bool       `json:"confirmed"`
	ConfirmedBy string     `json:"confirmed_by,omitempty"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	Note        string     `json:"note,omitempty"`
}

// PreCheckInput 是复役前重新执行的运行前校验数据。
type PreCheckInput struct {
	Insulation  float64 `json:"insulation_mohm"`
	Vibration   float64 `json:"vibration_mm_s"`
	Temperature float64 `json:"temperature_c"`
	Operator    string  `json:"operator"`
	Note        string  `json:"note,omitempty"`
}

// PreCheck 记录一次运行前校验的结果。
type PreCheck struct {
	Passed      bool       `json:"passed"`
	Insulation  float64    `json:"insulation_mohm"`
	Vibration   float64    `json:"vibration_mm_s"`
	Temperature float64    `json:"temperature_c"`
	Operator    string     `json:"operator"`
	Note        string     `json:"note,omitempty"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
}

func (in PreCheckInput) validate() error {
	if strings.TrimSpace(in.Operator) == "" {
		return errors.New("pre-start check operator is required")
	}
	if in.Insulation < MinInsulationMOhm {
		return errors.New("insulation resistance is below the safe limit")
	}
	if in.Vibration < 0 || in.Vibration > MaxVibrationMMS {
		return errors.New("vibration is outside the safe range")
	}
	if in.Temperature < 0 || in.Temperature > MaxTemperatureC {
		return errors.New("winding temperature is outside the safe range")
	}
	return nil
}

func (in PreCheckInput) evaluate(now time.Time) PreCheck {
	stamp := now.UTC()
	return PreCheck{
		Passed:      in.validate() == nil,
		Insulation:  in.Insulation,
		Vibration:   in.Vibration,
		Temperature: in.Temperature,
		Operator:    strings.TrimSpace(in.Operator),
		Note:        strings.TrimSpace(in.Note),
		CheckedAt:   &stamp,
	}
}

// ChecksFor 返回某类设备的现场安全检查项模板（供界面与登记使用）。
func ChecksFor(kind model.DeviceKind) []SafetyCheck {
	templates := safetyCheckTemplates(kind)
	result := make([]SafetyCheck, len(templates))
	copy(result, templates)
	return result
}

// PreCheckRules 描述运行前校验的阈值规则，供界面展示。
type PreCheckRules struct {
	MinInsulationMOhm float64 `json:"min_insulation_mohm"`
	MaxVibrationMMS   float64 `json:"max_vibration_mm_s"`
	MaxTemperatureC   float64 `json:"max_temperature_c"`
}

// PreCheckRuleSet 返回当前运行前校验阈值。
func PreCheckRuleSet() PreCheckRules {
	return PreCheckRules{
		MinInsulationMOhm: MinInsulationMOhm,
		MaxVibrationMMS:   MaxVibrationMMS,
		MaxTemperatureC:   MaxTemperatureC,
	}
}

// safetyCheckTemplates 返回按设备类型区分的现场安全检查项模板。
func safetyCheckTemplates(kind model.DeviceKind) []SafetyCheck {
	switch kind {
	case model.DeviceBlower:
		return []SafetyCheck{
			{Code: "POWER_ISOLATED", Label: "风机电源已断开并挂牌上锁"},
			{Code: "IMPELLER_STOPPED", Label: "叶轮已完全停止且无惰转"},
			{Code: "PRESSURE_RELEASED", Label: "风管余压已泄放"},
			{Code: "GUARD_FASTENED", Label: "联轴器防护罩复位紧固"},
		}
	case model.DevicePump:
		return []SafetyCheck{
			{Code: "POWER_ISOLATED", Label: "泵电源已断开并挂牌上锁"},
			{Code: "IMPELLER_STOPPED", Label: "泵轴已完全停止"},
			{Code: "PIPELINE_DEPRESSURIZED", Label: "进出水管路已泄压"},
			{Code: "SUCTION_CLOSED", Label: "吸入侧阀门已关闭"},
		}
	case model.DeviceValve:
		return []SafetyCheck{
			{Code: "POWER_ISOLATED", Label: "阀门电动/气动动力源已隔离"},
			{Code: "PIPELINE_DEPRESSURIZED", Label: "阀前管路已泄压"},
			{Code: "UPSTREAM_BLOCKED", Label: "上游已加盲板封堵"},
			{Code: "BYPASS_VERIFIED", Label: "旁通流程已确认"},
		}
	default:
		return []SafetyCheck{
			{Code: "POWER_ISOLATED", Label: "设备动力源已隔离并挂牌"},
		}
	}
}

package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/lockout"
	"github.com/wyw14/cry-97/internal/model"
)

type lockoutRequest struct {
	DeviceID string `json:"device_id"`
	Reason   string `json:"reason"`
	Owner    string `json:"owner"`
}

type checkRequest struct {
	Code     string `json:"code"`
	Operator string `json:"operator"`
	Note     string `json:"note"`
}

type preCheckRequest struct {
	Insulation  float64 `json:"insulation_mohm"`
	Vibration   float64 `json:"vibration_mm_s"`
	Temperature float64 `json:"temperature_c"`
	Operator    string  `json:"operator"`
	Note        string  `json:"note"`
}

type restoreRequest struct {
	Operator string `json:"operator"`
}

func (s *Server) deviceList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"devices": s.plant.Devices()})
}

func (s *Server) lockoutList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"lockouts":       s.plant.Lockouts(),
		"active":         s.plant.ActiveLockouts(),
		"active_count":   len(s.plant.ActiveLockouts()),
		"check_library":  checkLibrary(),
		"precheck_rules": lockout.PreCheckRuleSet(),
	})
}

func (s *Server) lockDevice(w http.ResponseWriter, r *http.Request) {
	var input lockoutRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tag, err := s.plant.LockDevice(r.Context(), input.DeviceID, input.Reason, input.Owner)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusCreated, tag)
}

func (s *Server) confirmLockoutCheck(w http.ResponseWriter, r *http.Request) {
	tagID, err := uuid.Parse(chi.URLParam(r, "tagID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var input checkRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tag, err := s.plant.ConfirmLockoutCheck(r.Context(), tagID, input.Code, input.Operator, input.Note)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, tag)
}

func (s *Server) submitLockoutPreCheck(w http.ResponseWriter, r *http.Request) {
	tagID, err := uuid.Parse(chi.URLParam(r, "tagID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var input preCheckRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tag, err := s.plant.SubmitLockoutPreCheck(r.Context(), tagID, lockout.PreCheckInput{
		Insulation: input.Insulation, Vibration: input.Vibration,
		Temperature: input.Temperature, Operator: input.Operator, Note: input.Note,
	})
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, tag)
}

func (s *Server) restoreDevice(w http.ResponseWriter, r *http.Request) {
	tagID, err := uuid.Parse(chi.URLParam(r, "tagID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var input restoreRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tag, err := s.plant.RestoreDevice(r.Context(), tagID, input.Operator)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, tag)
}

func checkLibrary() map[string][]lockout.SafetyCheck {
	return map[string][]lockout.SafetyCheck{
		"blower": lockout.ChecksFor(model.DeviceBlower),
		"pump":   lockout.ChecksFor(model.DevicePump),
		"valve":  lockout.ChecksFor(model.DeviceValve),
	}
}

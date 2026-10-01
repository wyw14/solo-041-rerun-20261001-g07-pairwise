package monitor

import (
	"sort"

	"github.com/wyw14/cry-97/internal/model"
)

type LineSummary struct {
	LineID       model.LineID       `json:"line_id"`
	Stage        model.ProcessStage `json:"stage"`
	Generation   uint64             `json:"generation"`
	Emergency    bool               `json:"emergency"`
	BatchID      string             `json:"batch_id,omitempty"`
	LockoutCount int                `json:"lockout_count"`
}

func SummarizeLines(lines []model.LineState, lockoutCounts map[model.LineID]int) []LineSummary {
	result := make([]LineSummary, 0, len(lines))
	for _, line := range lines {
		result = append(result, LineSummary{
			LineID: line.ID, Stage: line.Stage, Generation: line.Generation,
			Emergency: line.Emergency, BatchID: line.BatchID,
			LockoutCount: lockoutCounts[line.ID],
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LineID < result[j].LineID })
	return result
}

func CountStages(lines []model.LineState) map[model.ProcessStage]int {
	result := make(map[model.ProcessStage]int)
	for _, line := range lines {
		result[line.Stage]++
	}
	return result
}

package processlist

import (
	"context"
	"sort"
	"strings"
)

// Process is the minimal stable information needed by the process-loopback
// picker. Executable is the image file name reported by the operating system.
type Process struct {
	PID        uint32 `json:"pid"`
	Executable string `json:"executable"`
}

func List(ctx context.Context) ([]Process, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	values, err := list(ctx)
	if err != nil {
		return nil, err
	}
	return normalize(values), nil
}

func normalize(values []Process) []Process {
	result := make([]Process, 0, len(values))
	seen := make(map[uint32]struct{}, len(values))
	for _, value := range values {
		value.Executable = strings.TrimSpace(value.Executable)
		if value.PID == 0 || value.Executable == "" {
			continue
		}
		if _, exists := seen[value.PID]; exists {
			continue
		}
		seen[value.PID] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := strings.ToLower(result[i].Executable), strings.ToLower(result[j].Executable)
		if left == right {
			return result[i].PID < result[j].PID
		}
		return left < right
	})
	return result
}

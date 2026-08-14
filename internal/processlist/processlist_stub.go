//go:build !windows

package processlist

import "context"

func list(context.Context) ([]Process, error) { return []Process{}, nil }

package command

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Config is the legacy KSpeech command-recognizer configuration shape.
// Field names intentionally retain their original JSON casing.
type Config struct {
	Command          string `json:"Command"`
	Arguments        string `json:"Arguments"`
	WorkingDirectory string `json:"WorkingDirectory"`
	LogFile          string `json:"LogFile"`
}

func decodeConfig(data []byte) (Config, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Config{}, nil
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, fmt.Errorf("decode command recognizer config: %w", err)
	}
	return config, nil
}

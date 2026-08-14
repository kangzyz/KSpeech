package config

import (
	"os"
	"path/filepath"
)

func Defaults() map[string]any {
	documents := documentsDirectory()

	return map[string]any{
		GeneralLanguage:        "zh-cn",
		GeneralLaunchOnStartup: false,
		// A clean Go package does not bundle a speech model. Existing user or
		// packaged defaults still override this value, but a first launch must
		// remain usable until the user installs and selects a recognizer model.
		GeneralStartOnLaunch:      false,
		GeneralAutoUpdate:         true,
		GeneralResultLogPath:      filepath.Join(documents, "KSpeechLogs"),
		GeneralMainWindowLocation: []any{},

		AppearanceShadowColor:     uint32(0xFF000000),
		AppearanceShadowSize:      10,
		AppearanceFontFamily:      "Arial",
		AppearanceFontSize:        48,
		AppearanceFontColor:       uint32(0xFFFFFFFF),
		AppearanceMouseHover:      uint32(0x2709A9FF),
		AppearanceTextAlign:       0,
		AppearanceBackgroundColor: uint32(0x00000000),

		// Rule-based punctuation needs no extra download, so captions read as
		// sentences on a fresh install. The modes are defined by
		// internal/punctuation, which cannot be imported here: config is the
		// leaf package every other one depends on.
		PunctuationMode:      "rules",
		PunctuationModelPath: "",

		NotificationType:           1,
		NotificationSensitiveWords: "",
		NotificationShownLockUsage: false,

		// Recognition stays local by default: the assistant only reaches a
		// network endpoint after the user enables it and supplies one.
		AssistantEnabled:         false,
		AssistantEndpoint:        "",
		AssistantAPIKey:          "",
		AssistantModel:           "",
		AssistantSummarize:       true,
		AssistantSummaryInterval: 90,
		AssistantAutoAnswer:      true,
		// Once the assistant is on, the provider is already reading the
		// question; letting it search means the answer can cover anything that
		// happened after the model was trained.
		AssistantTools:            true,
		AssistantContextSentences: 30,
		AssistantBackground:       "",
		AssistantTimeout:          30,

		// One unlabeled input by default. Capturing the microphone alongside it
		// is a deliberate choice: it doubles the CPU cost and starts recording
		// the person at this computer.
		AudioSource:      PluginKey(AudioSourceWindowsModule, LoopbackAudioSourceID),
		AudioChannels:    "",
		RecognizerSource: PluginKey(SherpaOnnxModule, SherpaOnnxID),
	}
}

func fallbackDocumentsDirectory() string {
	documents, err := os.UserHomeDir()
	if err != nil {
		documents = "."
	}
	return filepath.Join(documents, "Documents")
}

package config

const (
	GeneralLanguage           = "general.Language"
	GeneralLaunchOnStartup    = "general.LaunchOnStartup"
	GeneralStartOnLaunch      = "general.StartOnLaunch"
	GeneralAutoUpdate         = "general.AutoUpdate"
	GeneralResultLogPath      = "general.ResultLogPath"
	GeneralMainWindowLocation = "general.MainWindowLocation"

	AppearanceShadowColor     = "appearance.ShadowColor"
	AppearanceShadowSize      = "appearance.ShadowSize"
	AppearanceFontFamily      = "appearance.FontFamily"
	AppearanceFontSize        = "appearance.FontSize"
	AppearanceFontColor       = "appearance.FontColor"
	AppearanceMouseHover      = "appearance.MouseHover"
	AppearanceTextAlign       = "appearance.TextAlign"
	AppearanceBackgroundColor = "appearance.BackgroundColor"

	// Streaming recognizers emit bare text, so KSpeech punctuates finished
	// sentences itself. PunctuationModelPath is only read in the model mode.
	PunctuationMode      = "punctuation.Mode"
	PunctuationModelPath = "punctuation.ModelPath"

	NotificationType           = "notification.NotificationType"
	NotificationSensitiveWords = "notification.SensitiveWords"
	NotificationShownLockUsage = "notification.ShownLockUsage"

	// The assistant talks to a user-supplied OpenAI-compatible endpoint, so it
	// stays off until the user turns it on: enabling it is what allows
	// recognized text to leave the machine.
	AssistantEnabled         = "assistant.Enabled"
	AssistantEndpoint        = "assistant.Endpoint"
	AssistantAPIKey          = "assistant.ApiKey"
	AssistantModel           = "assistant.Model"
	AssistantSummarize       = "assistant.Summarize"
	AssistantSummaryInterval = "assistant.SummaryIntervalSeconds"
	AssistantAutoAnswer      = "assistant.AutoAnswer"
	// AssistantTools declares the model provider's own hosted tools — web
	// search above all — on answer requests. The provider runs them, so this
	// widens what the provider sees only to the question already being sent.
	AssistantTools            = "assistant.Tools"
	AssistantContextSentences = "assistant.ContextSentences"
	AssistantBackground       = "assistant.Background"
	AssistantTimeout          = "assistant.TimeoutSeconds"

	// AudioSource is the legacy single-input key. It stays the fallback for
	// configurations written before multi-input capture, and is kept pointing at
	// the first channel so an old reader still sees a usable value.
	AudioSource = "audio.source"
	// AudioChannels holds the labeled audio inputs captured together, as a JSON
	// array of config.Channel. Empty means "use AudioSource".
	AudioChannels    = "audio.channels"
	RecognizerSource = "recognizer.source"
)

const (
	AudioSourceWindowsModule = "KSpeech.AudioSource.Windows"
	LoopbackAudioSourceID    = "F32B7F03-7030-4960-A8DF-96377C8B5FDD"
	MicrophoneAudioSourceID  = "3746756F-07D8-4972-BBF7-C443DF1E7E24"
	ProcessAudioSourceID     = "CE70909A-DBFC-4FF2-8059-30DDCFBDDF78"

	SherpaOnnxModule = "KSpeech.Recognizer.SherpaOnnx"
	SherpaOnnxID     = "3002EE6C-9770-419F-A745-E3148747AF4C"
	SherpaNcnnModule = "KSpeech.Recognizer.SherpaNcnn"
	SherpaNcnnID     = "94C23641-CBE0-42B6-9654-82DA42D519F3"
	CommandModule    = "KSpeech.Recognizer.Command"
	CommandID        = "A1B2C3D4-5E6F-7890-ABCD-EF1234567890"
)

func PluginKey(moduleID, pluginID string) string {
	return escapeModuleID(moduleID) + "!" + pluginID
}

func PluginConfigKey(pluginKey string) string {
	return "plugin." + pluginKey + ".config"
}

func escapeModuleID(moduleID string) string {
	// Preserve PluginManager.GetFullKey from the .NET implementation: colons
	// are escaped first, then dots become single colons.
	var out []rune
	for _, r := range moduleID {
		switch r {
		case ':':
			out = append(out, ':', ':')
		case '.':
			out = append(out, ':')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

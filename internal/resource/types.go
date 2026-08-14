// Package resource discovers, downloads, installs, and removes KSpeech modules.
//
// Local manifests are decoded leniently because built-in plugins may omit
// marketplace-only fields such as type and install.
package resource

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const (
	PluginDirName  = "plugins"
	ModuleJSONName = "ksmodule.json"
	// DefaultMarketURL points at the catalogue in this repository, forked from
	// the upstream community index. Only the index is served here: every
	// install step carries an absolute URL to the model archive on the
	// sherpa-onnx releases, so hosting it costs one small JSON file.
	DefaultMarketURL = "https://raw.githubusercontent.com/kangzyz/KSpeech/master/marketplace/marketplace.json"
	// defaultHTTPTimeout bounds one attempt at reading the index. The file is a
	// few kilobytes, so the budget is entirely about reaching a host that can
	// be slow or briefly unreachable from some networks; ten seconds turned a
	// recoverable hiccup into a failed refresh.
	defaultHTTPTimeout = 20 * time.Second
	// marketplaceAttempts retries a failed index read once. The common failure
	// is a connection that never completes rather than a rejection, and the
	// second attempt costs nothing when the first succeeds.
	marketplaceAttempts = 2
	// marketplaceRetryDelay lets a flapping route settle before retrying.
	marketplaceRetryDelay = 500 * time.Millisecond

	// DefaultMaxDownloadBytes limits each download step independently. Large
	// model packages can override it through Options.MaxDownloadBytes.
	DefaultMaxDownloadBytes int64 = 2 << 30
	// DefaultMaxInstallBytes limits the cumulative bytes emitted by built-in
	// extract and write steps for one installation transaction.
	DefaultMaxInstallBytes int64 = 4 << 30
	// DefaultMaxTransactionDownloadBytes limits the sum of all artifact bytes
	// stored by one installation. This is independent of final extracted size.
	DefaultMaxTransactionDownloadBytes int64 = 4 << 30
	// DefaultMaxInstallSteps bounds marketplace work before any network or disk
	// mutation. Callers may select a lower limit but not disable it.
	DefaultMaxInstallSteps = 256
)

const (
	ModuleTypePlugin           = "plugin"
	ModuleTypeSherpaOnnxModel  = "sherpaonnx_model"
	ModuleTypeSherpaNcnnModel  = "sherpancnn_model"
	ModuleTypePunctuationModel = "punctuation_model"
)

const (
	InstallStepDownload        = "download"
	InstallStepExtract         = "extract"
	InstallStepSaveFile        = "save_file"
	InstallStepWriteFile       = "write_file"
	InstallStepWriteModuleJSON = "write_module_json"
)

var (
	ErrChecksumMismatch   = errors.New("resource checksum mismatch")
	ErrInvalidModule      = errors.New("invalid resource module")
	ErrNotInstalled       = errors.New("resource is not installed")
	ErrNotRemovable       = errors.New("resource is built in and cannot be removed")
	ErrUnsafePath         = errors.New("unsafe resource path")
	ErrUnsupportedArchive = errors.New("unsupported resource archive")
	ErrInsecureTransport  = errors.New("insecure resource transport")
	ErrDownloadLimit      = errors.New("resource download exceeds byte limit")
	ErrInstallSizeLimit   = errors.New("resource installation exceeds byte limit")
)

// ModuleInfo mirrors the legacy ModuleInfo DTO. Fields that were nullable in
// the old application use their Go zero value; unknown JSON fields remain
// accepted for forward and backward compatibility.
type ModuleInfo struct {
	ID                  string                    `json:"id"`
	Version             int64                     `json:"version"`
	Desc                string                    `json:"desc,omitempty"`
	UpdateDesc          string                    `json:"updateDesc,omitempty"`
	DisplayVersion      string                    `json:"displayVersion,omitempty"`
	Name                string                    `json:"name,omitempty"`
	Author              string                    `json:"author,omitempty"`
	Publisher           string                    `json:"publisher,omitempty"`
	Homepage            string                    `json:"homepage,omitempty"`
	Repository          string                    `json:"repository,omitempty"`
	Type                string                    `json:"type,omitempty"`
	APILevel            *int                      `json:"apiLevel,omitempty"`
	Assemblies          []string                  `json:"assemblies,omitempty"`
	SherpaOnnxModelPath *SherpaOnnxModelPathInfo  `json:"sherpaonnx,omitempty"`
	SherpaNcnnModelPath *SherpaNcnnModelPathInfo  `json:"sherpancnn,omitempty"`
	PunctuationPath     *PunctuationModelPathInfo `json:"punctuation,omitempty"`
	InstallSteps        []InstallStep             `json:"install,omitempty"`
}

// PunctuationModelPathInfo points at the CT-Transformer model file inside a
// punctuation resource. The punctuation pass loads a single ONNX file, so one
// path is the whole contract.
type PunctuationModelPathInfo struct {
	ModelPath string `json:"model,omitempty"`
}

type SherpaOnnxModelPathInfo struct {
	EncoderPath string `json:"encoder,omitempty"`
	DecoderPath string `json:"decoder,omitempty"`
	JoinerPath  string `json:"joiner,omitempty"`
	TokenPath   string `json:"token,omitempty"`
}

// SherpaNcnnModelPathInfo is an optional Go-era extension for the seven-file
// NCNN transducer layout. Legacy manifests without it remain readable and are
// resolved by the application only when one unambiguous conventional layout
// exists below the module root.
type SherpaNcnnModelPathInfo struct {
	EncoderParamPath string `json:"encoder_param,omitempty"`
	EncoderBinPath   string `json:"encoder_bin,omitempty"`
	DecoderParamPath string `json:"decoder_param,omitempty"`
	DecoderBinPath   string `json:"decoder_bin,omitempty"`
	JoinerParamPath  string `json:"joiner_param,omitempty"`
	JoinerBinPath    string `json:"joiner_bin,omitempty"`
	TokenPath        string `json:"tokens,omitempty"`
}

type InstallStep struct {
	Type        string `json:"type"`
	DownloadURL string `json:"url,omitempty"`
	// SHA256 is optional for legacy marketplace compatibility. When present it
	// verifies the exact downloaded artifact bytes, but it is only as trusted as
	// the metadata carrying the digest and does not replace HTTPS transport.
	SHA256       string `json:"sha256,omitempty"`
	ExtractStep  *int   `json:"extractStep,omitempty"`
	ExtractType  string `json:"extractType,omitempty"`
	WriteContent string `json:"writeContent,omitempty"`
	WritePath    string `json:"writePath,omitempty"`
	ExtractTo    string `json:"extractTo,omitempty"`
	// SaveStep and SavePath drive save_file, which stores a downloaded artifact
	// under the module unchanged. Models published as loose files instead of one
	// archive need it: extract is otherwise the only step that can move a
	// download into the module directory, and it requires an archive. SaveStep
	// defaults to the immediately preceding step, matching ExtractStep.
	SaveStep *int   `json:"saveStep,omitempty"`
	SavePath string `json:"savePath,omitempty"`
}

type Marketplace struct {
	Version int          `json:"version"`
	Modules []ModuleInfo `json:"modules"`
}

// Resource is the merged local/marketplace view of one module. Built-in
// resources have CanRemove=false; resources beneath the user data directory
// have CanRemove=true.
type Resource struct {
	CanRemove  bool
	LocalInfo  *ModuleInfo
	LocalDir   string
	RemoteInfo *ModuleInfo
}

// EffectiveInfo matches the legacy UI behavior: marketplace metadata is used
// when it is available, otherwise the installed manifest is used.
func (r Resource) EffectiveInfo() *ModuleInfo {
	if r.RemoteInfo != nil {
		return r.RemoteInfo
	}
	return r.LocalInfo
}

func (r Resource) ID() string {
	if info := r.EffectiveInfo(); info != nil {
		return info.ID
	}
	return ""
}

func (r Resource) IsLocal() bool { return r.LocalInfo != nil }

func (r Resource) IsPlugin() bool {
	info := r.EffectiveInfo()
	return info != nil && info.Type == ModuleTypePlugin
}

func (r Resource) NeedsUpdate() bool {
	return r.LocalInfo != nil && r.RemoteInfo != nil && r.RemoteInfo.Version > r.LocalInfo.Version
}

// ProgressStage identifies the current transactional installation phase.
type ProgressStage string

const (
	ProgressPreparing   ProgressStage = "preparing"
	ProgressDownloading ProgressStage = "downloading"
	ProgressExtracting  ProgressStage = "extracting"
	ProgressWriting     ProgressStage = "writing"
	ProgressActivating  ProgressStage = "activating"
	ProgressComplete    ProgressStage = "complete"
)

type Progress struct {
	Stage      ProgressStage
	Step       int
	TotalSteps int
	Completed  int64
	Total      int64
}

type ProgressFunc func(Progress)

// Extractor is an extension point for an archive format not handled by the Go
// standard library. Implementations are trusted code: their final output is
// checked for symlinks, special files, and Options.MaxInstallBytes before
// activation, but an extractor itself must confine and limit writes while it
// runs. Built-in extractors enforce the shared byte budget before every write.
type Extractor func(ctx context.Context, archivePath, destination string) error

type Options struct {
	// ExecutableDir is the directory containing the application executable.
	// Its immediate plugins child is treated as the built-in resource root.
	ExecutableDir string
	// UserDataDir is the application data directory. Its immediate plugins
	// child is the removable resource root.
	UserDataDir        string
	MarketplaceURL     string
	HTTPClient         *http.Client
	MarketplaceTimeout time.Duration
	// AllowInsecureHTTP permits plaintext HTTP marketplace and artifact URLs.
	// It is intended only for explicitly configured local development servers;
	// production callers should retain the HTTPS-only default.
	AllowInsecureHTTP bool
	// MaxDownloadBytes is the maximum body size of each individual download
	// step. Zero selects DefaultMaxDownloadBytes; negative values are invalid.
	MaxDownloadBytes int64
	// MaxTransactionDownloadBytes is the cumulative artifact byte limit across
	// every download step in one Install call. Zero selects the default;
	// negative values are invalid. It does not count extracted or written files.
	MaxTransactionDownloadBytes int64
	// MaxInstallBytes is the cumulative number of bytes the built-in extract,
	// write_file, and write_module_json steps may emit in one transaction. Zero
	// selects DefaultMaxInstallBytes; negative values are invalid.
	MaxInstallBytes int64
	// MaxInstallSteps is the maximum number of install plan entries accepted.
	// Zero selects DefaultMaxInstallSteps; negative values are invalid.
	MaxInstallSteps int
	Extractors      map[string]Extractor
	// OnIssue receives non-fatal scan issues, such as a malformed manifest.
	// The affected directory is skipped and other resources remain available.
	OnIssue func(error)
}

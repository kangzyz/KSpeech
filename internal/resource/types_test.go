package resource

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestModuleInfoLegacyJSONNamesAndRoundTrip(t *testing.T) {
	const document = `{
		"id":"legacy.plugin",
		"version":42,
		"desc":"description",
		"updateDesc":"release notes",
		"displayVersion":"4.2.0",
		"name":"Legacy Plugin",
		"author":"Alice",
		"publisher":"Example Org",
		"homepage":"https://example.test/home",
		"repository":"https://example.test/repository",
		"type":"plugin",
		"apiLevel":7,
		"assemblies":["Legacy.Plugin.dll"],
		"sherpaonnx":{
			"encoder":"models/encoder.onnx",
			"decoder":"models/decoder.onnx",
			"joiner":"models/joiner.onnx",
			"token":"models/tokens.txt"
		},
		"sherpancnn":{
			"encoder_param":"ncnn/encoder.param",
			"encoder_bin":"ncnn/encoder.bin",
			"decoder_param":"ncnn/decoder.param",
			"decoder_bin":"ncnn/decoder.bin",
			"joiner_param":"ncnn/joiner.param",
			"joiner_bin":"ncnn/joiner.bin",
			"tokens":"ncnn/tokens.txt"
		},
		"install":[{
			"type":"download",
			"url":"https://example.test/plugin.zip",
			"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"extractStep":0,
			"extractType":"zip",
			"writeContent":"enabled=true",
			"writePath":"config/plugin.ini",
			"extractTo":"payload"
		}],
		"futureField":"ignored"
	}`

	var decoded ModuleInfo
	if err := json.Unmarshal([]byte(document), &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded.ID != "legacy.plugin" || decoded.Version != 42 || decoded.UpdateDesc != "release notes" || decoded.DisplayVersion != "4.2.0" {
		t.Fatalf("legacy scalar fields decoded incorrectly: %+v", decoded)
	}
	if decoded.APILevel == nil || *decoded.APILevel != 7 {
		t.Fatalf("APILevel = %v, want 7", decoded.APILevel)
	}
	if decoded.SherpaOnnxModelPath == nil || decoded.SherpaOnnxModelPath.TokenPath != "models/tokens.txt" {
		t.Fatalf("SherpaOnnxModelPath = %+v", decoded.SherpaOnnxModelPath)
	}
	if decoded.SherpaNcnnModelPath == nil || decoded.SherpaNcnnModelPath.EncoderParamPath != "ncnn/encoder.param" {
		t.Fatalf("SherpaNcnnModelPath = %+v", decoded.SherpaNcnnModelPath)
	}
	if len(decoded.InstallSteps) != 1 || decoded.InstallSteps[0].ExtractStep == nil || *decoded.InstallSteps[0].ExtractStep != 0 {
		t.Fatalf("InstallSteps = %+v", decoded.InstallSteps)
	}

	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &topLevel); err != nil {
		t.Fatalf("unmarshal encoded object: %v", err)
	}
	for _, key := range []string{"id", "version", "updateDesc", "displayVersion", "apiLevel", "sherpaonnx", "sherpancnn", "install"} {
		if _, exists := topLevel[key]; !exists {
			t.Errorf("encoded JSON is missing legacy key %q: %s", key, encoded)
		}
	}
	for _, incorrect := range []string{"UpdateDesc", "DisplayVersion", "APILevel", "SherpaOnnxModelPath", "InstallSteps"} {
		if _, exists := topLevel[incorrect]; exists {
			t.Errorf("encoded JSON unexpectedly contains Go field name %q: %s", incorrect, encoded)
		}
	}

	var roundTrip ModuleInfo
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("unmarshal round-trip JSON: %v", err)
	}
	if !reflect.DeepEqual(roundTrip, decoded) {
		t.Fatalf("round trip changed ModuleInfo\n got: %#v\nwant: %#v", roundTrip, decoded)
	}
}

func TestResourceDerivedStateUsesRemoteMetadata(t *testing.T) {
	local := &ModuleInfo{ID: "module", Version: 2, Type: ModuleTypeSherpaOnnxModel}
	remote := &ModuleInfo{ID: "module", Version: 3, Type: ModuleTypePlugin}
	resource := Resource{CanRemove: true, LocalInfo: local, RemoteInfo: remote}

	if resource.EffectiveInfo() != remote {
		t.Fatal("EffectiveInfo() did not prefer marketplace metadata")
	}
	if resource.ID() != "module" || !resource.IsLocal() || !resource.IsPlugin() || !resource.NeedsUpdate() {
		t.Fatalf("unexpected derived state: ID=%q IsLocal=%v IsPlugin=%v NeedsUpdate=%v", resource.ID(), resource.IsLocal(), resource.IsPlugin(), resource.NeedsUpdate())
	}

	localOnly := Resource{LocalInfo: local}
	if localOnly.EffectiveInfo() != local || localOnly.NeedsUpdate() {
		t.Fatalf("unexpected local-only state: %+v", localOnly)
	}
}

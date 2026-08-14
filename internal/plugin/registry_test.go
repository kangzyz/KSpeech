package plugin

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakePlugin struct {
	metadata Metadata
}

func (p *fakePlugin) Metadata() Metadata         { return p.metadata }
func (p *fakePlugin) Available() bool            { return true }
func (p *fakePlugin) LoadConfig([]byte) error    { return nil }
func (p *fakePlugin) Init(context.Context) error { return nil }
func (p *fakePlugin) Close() error               { return nil }

type fakeRecognizer struct {
	fakePlugin
}

func (p *fakeRecognizer) Start(context.Context) error      { return nil }
func (p *fakeRecognizer) Stop() error                      { return nil }
func (p *fakeRecognizer) NeedsAudio() bool                 { return true }
func (p *fakeRecognizer) Feed([]float32) error             { return nil }
func (p *fakeRecognizer) SetCallbacks(RecognizerCallbacks) {}

func TestRegistryUsesExactCallerKey(t *testing.T) {
	// Registry's zero value is ready to use; NewRegistry is a convenience.
	registry := &Registry{}
	instance := &fakePlugin{metadata: Metadata{ID: "plugin-id", Name: "example"}}
	const key = "module::id!plugin-id"

	if err := registry.Register(key, instance); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	got, ok := registry.Get(key)
	if !ok || got != instance {
		t.Fatalf("Get(%q) = (%v, %v), want (%v, true)", key, got, ok, instance)
	}
	if _, rewritten := registry.Get("module:id!plugin-id"); rewritten {
		t.Fatal("registry unexpectedly rewrote the caller-supplied key")
	}
}

func TestRegistryRejectsInvalidRegistrations(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("  ", &fakePlugin{}); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("empty-key error = %v, want ErrEmptyKey", err)
	}
	if err := registry.Register("nil", nil); !errors.Is(err, ErrNilPlugin) {
		t.Fatalf("nil error = %v, want ErrNilPlugin", err)
	}
	var typedNil *fakePlugin
	if err := registry.Register("typed-nil", typedNil); !errors.Is(err, ErrNilPlugin) {
		t.Fatalf("typed-nil error = %v, want ErrNilPlugin", err)
	}

	instance := &fakePlugin{}
	if err := registry.Register("duplicate", instance); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	if err := registry.Register("duplicate", &fakePlugin{}); !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("duplicate error = %v, want ErrDuplicateKey", err)
	}
	got, _ := registry.Get("duplicate")
	if got != instance {
		t.Fatal("duplicate registration replaced the existing plugin")
	}
}

func TestRegistrySnapshotsAndCapabilities(t *testing.T) {
	registry := NewRegistry()
	plain := &fakePlugin{}
	recognizer := &fakeRecognizer{}
	if err := registry.Register("z-plain", plain); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("a-recognizer", recognizer); err != nil {
		t.Fatal(err)
	}

	if got, want := registry.Keys(), []string{"a-recognizer", "z-plain"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
	gotRecognizer, ok := registry.Recognizer("a-recognizer")
	if !ok || gotRecognizer != recognizer {
		t.Fatalf("Recognizer() = (%v, %v), want (%v, true)", gotRecognizer, ok, recognizer)
	}
	if _, ok := registry.Recognizer("z-plain"); ok {
		t.Fatal("plain plugin was reported as a recognizer")
	}
	if got := registry.Recognizers(); len(got) != 1 || got["a-recognizer"] != recognizer {
		t.Fatalf("Recognizers() = %v", got)
	}

	snapshot := registry.Plugins()
	delete(snapshot, "z-plain")
	if _, ok := registry.Get("z-plain"); !ok {
		t.Fatal("mutating Plugins() snapshot changed the registry")
	}

	removed, ok := registry.Unregister("z-plain")
	if !ok || removed != plain {
		t.Fatalf("Unregister() = (%v, %v), want (%v, true)", removed, ok, plain)
	}
	if _, ok := registry.Get("z-plain"); ok {
		t.Fatal("unregistered plugin is still present")
	}
}

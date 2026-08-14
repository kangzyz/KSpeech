package plugin

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

var (
	// ErrEmptyKey indicates that a caller did not provide a registry key.
	ErrEmptyKey = errors.New("plugin registry key is empty")
	// ErrNilPlugin indicates an attempt to register a nil plugin.
	ErrNilPlugin = errors.New("plugin is nil")
	// ErrDuplicateKey indicates that a registry key is already occupied.
	ErrDuplicateKey = errors.New("plugin registry key is already registered")
)

// Registry is a concurrent, caller-keyed collection of plugins. It deliberately
// does not derive or rewrite keys; legacy module-ID escaping belongs to the
// configuration layer that constructs the key.
type Registry struct {
	mu      sync.RWMutex
	plugins map[string]Plugin
}

// NewRegistry returns an empty plugin registry.
func NewRegistry() *Registry {
	return &Registry{plugins: make(map[string]Plugin)}
}

// Register associates instance with the exact key supplied by the caller.
func (r *Registry) Register(key string, instance Plugin) error {
	if strings.TrimSpace(key) == "" {
		return ErrEmptyKey
	}
	if isNilPlugin(instance) {
		return ErrNilPlugin
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plugins == nil {
		r.plugins = make(map[string]Plugin)
	}
	if _, exists := r.plugins[key]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateKey, key)
	}
	r.plugins[key] = instance
	return nil
}

// Unregister removes and returns the plugin at key. It does not stop or close
// the plugin; lifecycle ownership stays with the caller.
func (r *Registry) Unregister(key string) (Plugin, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	instance, exists := r.plugins[key]
	if exists {
		delete(r.plugins, key)
	}
	return instance, exists
}

// Get returns the plugin registered at key.
func (r *Registry) Get(key string) (Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	instance, exists := r.plugins[key]
	return instance, exists
}

// Keys returns all registered keys in deterministic lexical order.
func (r *Registry) Keys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.plugins))
	for key := range r.plugins {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Plugins returns a point-in-time copy of all registry entries.
func (r *Registry) Plugins() map[string]Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]Plugin, len(r.plugins))
	for key, instance := range r.plugins {
		result[key] = instance
	}
	return result
}

// Recognizer returns the recognizer at key, if the plugin has that capability.
func (r *Registry) Recognizer(key string) (Recognizer, bool) {
	instance, exists := r.Get(key)
	if !exists {
		return nil, false
	}
	recognizer, ok := instance.(Recognizer)
	return recognizer, ok
}

// Recognizers returns a point-in-time copy containing only recognizers.
func (r *Registry) Recognizers() map[string]Recognizer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]Recognizer)
	for key, instance := range r.plugins {
		if recognizer, ok := instance.(Recognizer); ok {
			result[key] = recognizer
		}
	}
	return result
}

// AudioSource returns the audio source at key, if the plugin has that capability.
func (r *Registry) AudioSource(key string) (AudioSource, bool) {
	instance, exists := r.Get(key)
	if !exists {
		return nil, false
	}
	source, ok := instance.(AudioSource)
	return source, ok
}

// AudioSources returns a point-in-time copy containing only audio sources.
func (r *Registry) AudioSources() map[string]AudioSource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]AudioSource)
	for key, instance := range r.plugins {
		if source, ok := instance.(AudioSource); ok {
			result[key] = source
		}
	}
	return result
}

// Translators returns a point-in-time copy containing only translators.
func (r *Registry) Translators() map[string]Translator {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]Translator)
	for key, instance := range r.plugins {
		if translator, ok := instance.(Translator); ok {
			result[key] = translator
		}
	}
	return result
}

func isNilPlugin(instance Plugin) bool {
	if instance == nil {
		return true
	}
	value := reflect.ValueOf(instance)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

package backend

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// Tests of the embedded local model's TOPOLOGY / PLAN / TUNING surface:
// the additive EmbeddedLLMStatus fields, GetEmbeddedLLMTuning,
// SetEmbeddedLLMTuning and ProbeEmbeddedLLMDevices
// (backend/frontend_api_embedded.go).
//
// Scope: the boundary contract. The planner's own decisions (which shape fits,
// which relaxation fires, what a note says) belong to core/embeddedllm and are
// covered there; nothing here re-tests a plan, only that this layer renders one
// faithfully and refuses to write a tuning the next config load would reject.

// ── helpers ────────────────────────────────────────────────────────────────

func tuningIntPtr(v int) *int         { return &v }
func tuningBoolPtr(v bool) *bool      { return &v }
func tuningF64Ptr(v float64) *float64 { return &v }

// storedTuning reads the tuning section straight off the live config, bypassing
// the DTO, so a test can tell "the RPC reported X" from "the config holds X".
func storedTuning(t *testing.T, f *FrontendAPI) config.TuningConfig {
	t.Helper()
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	if f.config == nil {
		t.Fatal("config not initialized")
	}
	return f.config.EmbeddedLLM.Tuning
}

// ── the signature convention ───────────────────────────────────────────────

// TestEmbeddedLLMTuningRPCSignaturesFollowTheBoundaryConvention pins the three
// new methods to the documented boundary shape (desktop-frontend.md "RPC
// Surface").
//
// GetEmbeddedLLMTuning carries an error even though it is a read-only getter,
// and that is the convention's other half rather than an exception to it: the
// rule is "getters that CANNOT fail return T only", and this one has a real
// failure — no config yet — whose fail-soft answer (an all-nil DTO) would be
// indistinguishable from "the operator overrode nothing" and would let an editor
// offer a Save that wipes the real tuning.
func TestEmbeddedLLMTuningRPCSignaturesFollowTheBoundaryConvention(t *testing.T) {
	api := reflect.TypeOf(&FrontendAPI{})
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	self := reflect.TypeOf(&FrontendAPI{})

	t.Run("GetEmbeddedLLMTuning returns (DTO, error)", func(t *testing.T) {
		m, ok := api.MethodByName("GetEmbeddedLLMTuning")
		if !ok {
			t.Fatal("GetEmbeddedLLMTuning is not exported on *FrontendAPI")
		}
		if m.Type.NumIn() != 1 {
			t.Errorf("GetEmbeddedLLMTuning takes %d argument(s), want 0", m.Type.NumIn()-1)
		}
		if m.Type.NumOut() != 2 {
			t.Fatalf("GetEmbeddedLLMTuning returns %d value(s), want (EmbeddedLLMTuningDTO, error)", m.Type.NumOut())
		}
		if got := m.Type.Out(0); got != reflect.TypeOf(EmbeddedLLMTuningDTO{}) {
			t.Errorf("GetEmbeddedLLMTuning returns %s, want EmbeddedLLMTuningDTO", got)
		}
		if got := m.Type.Out(1); got != errorType {
			t.Errorf("GetEmbeddedLLMTuning's second result is %s, want error", got)
		}
	})

	t.Run("SetEmbeddedLLMTuning takes one request and returns only an error", func(t *testing.T) {
		m, ok := api.MethodByName("SetEmbeddedLLMTuning")
		if !ok {
			t.Fatal("SetEmbeddedLLMTuning is not exported on *FrontendAPI")
		}
		wantIn := []reflect.Type{self, reflect.TypeOf(EmbeddedLLMTuningRequest{})}
		if m.Type.NumIn() != len(wantIn) {
			t.Fatalf("SetEmbeddedLLMTuning takes %d argument(s), want (req EmbeddedLLMTuningRequest)", m.Type.NumIn()-1)
		}
		for i, want := range wantIn {
			if got := m.Type.In(i); got != want {
				t.Errorf("SetEmbeddedLLMTuning argument %d is %s, want %s", i, got, want)
			}
		}
		if m.Type.NumOut() != 1 || m.Type.Out(0) != errorType {
			t.Errorf("SetEmbeddedLLMTuning returns %s, want exactly error", m.Type)
		}
	})

	t.Run("ProbeEmbeddedLLMDevices returns (DTO, error)", func(t *testing.T) {
		m, ok := api.MethodByName("ProbeEmbeddedLLMDevices")
		if !ok {
			t.Fatal("ProbeEmbeddedLLMDevices is not exported on *FrontendAPI")
		}
		if m.Type.NumIn() != 1 {
			t.Errorf("ProbeEmbeddedLLMDevices takes %d argument(s), want 0", m.Type.NumIn()-1)
		}
		if m.Type.NumOut() != 2 {
			t.Fatalf("ProbeEmbeddedLLMDevices returns %d value(s), want (EmbeddedLLMDevicesDTO, error)", m.Type.NumOut())
		}
		if got := m.Type.Out(0); got != reflect.TypeOf(EmbeddedLLMDevicesDTO{}) {
			t.Errorf("ProbeEmbeddedLLMDevices returns %s, want EmbeddedLLMDevicesDTO", got)
		}
		if got := m.Type.Out(1); got != errorType {
			t.Errorf("ProbeEmbeddedLLMDevices's second result is %s, want error", got)
		}
	})

	t.Run("no tuning method was added to the lifecycle type", func(t *testing.T) {
		// Methods on FrontendAPILifecycle are never bound by Wails, so a tuning
		// RPC placed there would be silently unreachable from the UI.
		lifecycle := reflect.TypeOf(&FrontendAPILifecycle{})
		for _, name := range []string{
			"GetEmbeddedLLMTuning", "SetEmbeddedLLMTuning", "ProbeEmbeddedLLMDevices",
		} {
			if _, ok := lifecycle.MethodByName(name); ok {
				t.Errorf("%s is on *FrontendAPILifecycle, which Wails does not bind", name)
			}
		}
	})
}

// ── the getter ─────────────────────────────────────────────────────────────

// TestGetEmbeddedLLMTuningKeepsUnsetDistinguishableFromExplicitAuto is the
// property the whole DTO shape exists for. config.TuningConfig uses pointers
// because "the operator never wrote this" and "the operator wrote auto" are
// different persisted values; a DTO that collapsed them could not round-trip the
// section it mirrors, and a Save from that editor would silently rewrite the
// operator's config.
func TestGetEmbeddedLLMTuningKeepsUnsetDistinguishableFromExplicitAuto(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	f.config.EmbeddedLLM.Tuning = config.TuningConfig{
		// An EXPLICIT auto on two knobs...
		Context:     config.EmbeddedLLMContextConfig{Mode: strPtr(config.EmbeddedLLMTuningAuto)},
		KVCacheType: strPtr(config.EmbeddedLLMTuningAuto),
		// ...an explicit choice on two more...
		Offload: config.EmbeddedLLMOffloadConfig{
			Mode:   strPtr(config.EmbeddedLLMOffloadLayers),
			Layers: tuningIntPtr(12),
		},
		Parallel: tuningIntPtr(2),
		// ...and an explicit ZERO, which is a real value here (it disables the
		// prompt cache) and must not be read as unset.
		CacheRAMMiB: tuningIntPtr(0),
		// Fit, FitTargetMiB, FitMinContext, KVOffload, MMProjOffload, Packing
		// and HostReserveGiB stay UNSET.
	}

	dto, err := f.GetEmbeddedLLMTuning()
	if err != nil {
		t.Fatalf("GetEmbeddedLLMTuning: %v", err)
	}

	if dto.Context.Mode == nil || *dto.Context.Mode != config.EmbeddedLLMTuningAuto {
		t.Errorf("context.mode = %v, want the explicit %q", dto.Context.Mode, config.EmbeddedLLMTuningAuto)
	}
	if dto.KVCacheType == nil || *dto.KVCacheType != config.EmbeddedLLMTuningAuto {
		t.Errorf("kv_cache_type = %v, want the explicit %q", dto.KVCacheType, config.EmbeddedLLMTuningAuto)
	}
	if dto.CacheRAMMiB == nil || *dto.CacheRAMMiB != 0 {
		t.Errorf("cache_ram_mib = %v, want the explicit 0 (disable the cache)", dto.CacheRAMMiB)
	}
	if dto.Offload.Mode == nil || *dto.Offload.Mode != config.EmbeddedLLMOffloadLayers {
		t.Errorf("offload.mode = %v, want %q", dto.Offload.Mode, config.EmbeddedLLMOffloadLayers)
	}
	if dto.Offload.Layers == nil || *dto.Offload.Layers != 12 {
		t.Errorf("offload.layers = %v, want 12", dto.Offload.Layers)
	}
	if dto.Parallel == nil || *dto.Parallel != 2 {
		t.Errorf("parallel = %v, want 2", dto.Parallel)
	}

	for name, ptr := range map[string]any{
		"fit":              dto.Fit,
		"fit_target_mib":   dto.FitTargetMiB,
		"fit_min_context":  dto.FitMinContext,
		"kv_offload":       dto.KVOffload,
		"mmproj_offload":   dto.MMProjOffload,
		"packing":          dto.Packing,
		"host_reserve_gib": dto.HostReserveGiB,
		"context.tokens":   dto.Context.Tokens,
	} {
		if v := reflect.ValueOf(ptr); !v.IsNil() {
			t.Errorf("the unset knob %s came back as %v, want nil", name, ptr)
		}
	}
}

// TestGetEmbeddedLLMTuningDoesNotAliasTheLiveConfig pins the pointer copy: a DTO
// the UI holds across a config reload must not change underneath it, and writing
// through one of its pointers must not reach the config.
func TestGetEmbeddedLLMTuningDoesNotAliasTheLiveConfig(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	f.config.EmbeddedLLM.Tuning = config.TuningConfig{Parallel: tuningIntPtr(2)}

	dto, err := f.GetEmbeddedLLMTuning()
	if err != nil {
		t.Fatalf("GetEmbeddedLLMTuning: %v", err)
	}
	*dto.Parallel = 99

	if got := storedTuning(t, f).Parallel; got == nil || *got != 2 {
		t.Errorf("the stored parallel = %v, want it untouched at 2", got)
	}
	again, err := f.GetEmbeddedLLMTuning()
	if err != nil {
		t.Fatalf("GetEmbeddedLLMTuning (second read): %v", err)
	}
	if again.Parallel == nil || *again.Parallel != 2 {
		t.Errorf("a second read = %v, want 2", again.Parallel)
	}
}

// TestGetEmbeddedLLMTuningFailsRatherThanFabricatingAnEmptySection is the reason
// this getter returns an error at all: before startup there is nothing to read,
// and an all-nil DTO would be indistinguishable from "the operator overrode
// nothing".
func TestGetEmbeddedLLMTuningFailsRatherThanFabricatingAnEmptySection(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	f.configMu.Lock()
	f.config = nil
	f.configMu.Unlock()

	dto, err := f.GetEmbeddedLLMTuning()
	if err == nil {
		t.Fatalf("GetEmbeddedLLMTuning = %+v, want an error while no config is loaded", dto)
	}
	if !strings.Contains(err.Error(), "config not initialized") {
		t.Errorf("the error %q does not name the cause", err)
	}
}

// ── the setter: partial semantics ──────────────────────────────────────────

// TestSetEmbeddedLLMTuningKeepsEveryKnobTheRequestDoesNotName is the partial
// contract: nil means "keep", so a request that touches one knob cannot disturb
// the eleven others.
func TestSetEmbeddedLLMTuningKeepsEveryKnobTheRequestDoesNotName(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	stored := config.TuningConfig{
		Context:        config.EmbeddedLLMContextConfig{Mode: strPtr(config.EmbeddedLLMContextExact), Tokens: tuningIntPtr(32768)},
		KVCacheType:    strPtr("q8_0"),
		Offload:        config.EmbeddedLLMOffloadConfig{Mode: strPtr(config.EmbeddedLLMOffloadAll)},
		Fit:            tuningBoolPtr(false),
		FitTargetMiB:   tuningIntPtr(512),
		FitMinContext:  tuningIntPtr(8192),
		KVOffload:      tuningBoolPtr(false),
		MMProjOffload:  tuningBoolPtr(false),
		Packing:        strPtr("PTQ1_0"),
		Parallel:       tuningIntPtr(1),
		CacheRAMMiB:    tuningIntPtr(0),
		HostReserveGiB: tuningF64Ptr(6),
	}
	f.config.EmbeddedLLM.Tuning = stored

	// One knob, and only that knob.
	if err := f.SetEmbeddedLLMTuning(EmbeddedLLMTuningRequest{Parallel: tuningIntPtr(4)}); err != nil {
		t.Fatalf("SetEmbeddedLLMTuning: %v", err)
	}

	want := stored
	want.Parallel = tuningIntPtr(4)
	got := storedTuning(t, f)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the tuning after a one-knob patch:\n got %+v\nwant %+v", got, want)
	}
	// The untouched knobs must still be the SAME VALUES, not copies that lost
	// their explicitness on the way through.
	if got.Context.Tokens == nil || *got.Context.Tokens != 32768 {
		t.Errorf("context.tokens = %v, want it kept at 32768", got.Context.Tokens)
	}
	if got.CacheRAMMiB == nil || *got.CacheRAMMiB != 0 {
		t.Errorf("cache_ram_mib = %v, want it kept at the explicit 0", got.CacheRAMMiB)
	}

	// And it reached the file, not just memory.
	reloaded, err := config.Load(f.configPath)
	if err != nil {
		t.Fatalf("reloading the config: %v", err)
	}
	if !reflect.DeepEqual(reloaded.EmbeddedLLM.Tuning, want) {
		t.Errorf("the persisted tuning:\n got %+v\nwant %+v", reloaded.EmbeddedLLM.Tuning, want)
	}
}

// TestSetEmbeddedLLMTuningResetClearsAKnobBackToUnset covers the third state the
// pointer encoding cannot express on its own: "stop overriding this". Without
// Reset there would be no way to return a bool or int knob to the planner,
// because their nil already means "absent from this request".
func TestSetEmbeddedLLMTuningResetClearsAKnobBackToUnset(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	f.config.EmbeddedLLM.Tuning = config.TuningConfig{
		Context: config.EmbeddedLLMContextConfig{
			Mode: strPtr(config.EmbeddedLLMContextExact), Tokens: tuningIntPtr(32768),
		},
		Offload:        config.EmbeddedLLMOffloadConfig{Mode: strPtr(config.EmbeddedLLMOffloadCPU)},
		KVCacheType:    strPtr("q8_0"),
		Fit:            tuningBoolPtr(false),
		Parallel:       tuningIntPtr(4),
		HostReserveGiB: tuningF64Ptr(6),
	}

	if err := f.SetEmbeddedLLMTuning(EmbeddedLLMTuningRequest{
		Reset: []string{"context", "kv_cache_type", "parallel"},
	}); err != nil {
		t.Fatalf("SetEmbeddedLLMTuning: %v", err)
	}

	got := storedTuning(t, f)
	if got.Context.Mode != nil || got.Context.Tokens != nil {
		t.Errorf("context = %+v after a reset, want it fully unset", got.Context)
	}
	if got.KVCacheType != nil {
		t.Errorf("kv_cache_type = %v after a reset, want nil", *got.KVCacheType)
	}
	if got.Parallel != nil {
		t.Errorf("parallel = %v after a reset, want nil", *got.Parallel)
	}
	// The knobs the request never named survive a reset of the others.
	if got.Offload.Mode == nil || *got.Offload.Mode != config.EmbeddedLLMOffloadCPU {
		t.Errorf("offload.mode = %v, want it kept at %q", got.Offload.Mode, config.EmbeddedLLMOffloadCPU)
	}
	if got.Fit == nil || *got.Fit {
		t.Errorf("fit = %v, want it kept at the explicit false", got.Fit)
	}
	if got.HostReserveGiB == nil || *got.HostReserveGiB != 6 {
		t.Errorf("host_reserve_gib = %v, want it kept at 6", got.HostReserveGiB)
	}
}

// TestSetEmbeddedLLMTuningResetIsIdempotentOnAnAlreadyUnsetKnob: clearing what
// is already clear is not a change, so it must not rewrite config.yaml either.
func TestSetEmbeddedLLMTuningResetIsIdempotentOnAnAlreadyUnsetKnob(t *testing.T) {
	f, rec, _ := newEmbeddedTestAPI(t)
	if err := f.SetEmbeddedLLMTuning(EmbeddedLLMTuningRequest{Reset: []string{"fit"}}); err != nil {
		t.Fatalf("SetEmbeddedLLMTuning: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if n := rec.count(EventConfigUpdated); n != 0 {
		t.Errorf("config:updated was emitted %d time(s) for a no-op reset", n)
	}
}

// ── the setter: refusals ───────────────────────────────────────────────────

// TestSetEmbeddedLLMTuningRefusesAContradictoryRequest pins that "clear this"
// and "set this to that" in one request is reported rather than resolved by an
// invisible precedence rule.
func TestSetEmbeddedLLMTuningRefusesAContradictoryRequest(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	f.config.EmbeddedLLM.Tuning = config.TuningConfig{Parallel: tuningIntPtr(2)}

	err := f.SetEmbeddedLLMTuning(EmbeddedLLMTuningRequest{
		Reset:    []string{"parallel"},
		Parallel: tuningIntPtr(4),
	})
	if err == nil {
		t.Fatal("a request that both resets and sets parallel was accepted")
	}
	if !strings.Contains(err.Error(), "parallel") {
		t.Errorf("the error %q does not name the contradictory knob", err)
	}
	if got := storedTuning(t, f).Parallel; got == nil || *got != 2 {
		t.Errorf("the stored parallel = %v, want it untouched at 2", got)
	}
}

// TestSetEmbeddedLLMTuningRefusesAnUnknownResetKey: the reset vocabulary is the
// config-file one, so a typo is refused with the legal keys listed rather than
// silently ignored (an ignored reset key would look like a successful clear).
func TestSetEmbeddedLLMTuningRefusesAnUnknownResetKey(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	f.config.EmbeddedLLM.Tuning = config.TuningConfig{Parallel: tuningIntPtr(2)}

	err := f.SetEmbeddedLLMTuning(EmbeddedLLMTuningRequest{Reset: []string{"paralell"}})
	if err == nil {
		t.Fatal("an unknown reset key was accepted")
	}
	for _, want := range []string{"paralell", "parallel", "host_reserve_gib"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error %q does not mention %q", err, want)
		}
	}
	if got := storedTuning(t, f).Parallel; got == nil || *got != 2 {
		t.Errorf("the stored parallel = %v, want it untouched at 2", got)
	}
}

// TestSetEmbeddedLLMTuningRefusesInvalidValuesWithoutWriting is the criterion
// that matters most: every rejection happens BEFORE a byte of config.yaml
// changes, so a refused save leaves the file loadable. Each case is checked
// against both the in-memory section and the file on disk.
func TestSetEmbeddedLLMTuningRefusesInvalidValuesWithoutWriting(t *testing.T) {
	stored := config.TuningConfig{
		KVCacheType: strPtr("q8_0"),
		Parallel:    tuningIntPtr(1),
	}

	cases := []struct {
		name string
		req  EmbeddedLLMTuningRequest
	}{
		// q5_0 is excluded on a measured ~8x long-context decode slowdown, and
		// an unlisted precision is refused rather than coerced to f16.
		{"an unmodelled KV precision", EmbeddedLLMTuningRequest{KVCacheType: strPtr("q5_0")}},
		{"a made-up KV precision", EmbeddedLLMTuningRequest{KVCacheType: strPtr("int2")}},
		{"a made-up packing", EmbeddedLLMTuningRequest{Packing: strPtr("PQ9_9")}},
		{"a made-up offload mode", EmbeddedLLMTuningRequest{
			Offload: &EmbeddedLLMOffloadTuningRequest{Mode: strPtr("some")},
		}},
		{"a made-up context mode", EmbeddedLLMTuningRequest{
			Context: &EmbeddedLLMContextTuningRequest{Mode: strPtr("roughly")},
		}},
		// A zero-token window is not a configuration, it is a refusal.
		{"a zero explicit context", EmbeddedLLMTuningRequest{
			Context: &EmbeddedLLMContextTuningRequest{
				Mode: strPtr(config.EmbeddedLLMContextExact), Tokens: tuningIntPtr(0),
			},
		}},
		{"a negative explicit context", EmbeddedLLMTuningRequest{
			Context: &EmbeddedLLMContextTuningRequest{
				Mode: strPtr(config.EmbeddedLLMContextExact), Tokens: tuningIntPtr(-1),
			},
		}},
		// exact without a token count is the half-update the whole-knob
		// replacement semantics exist to catch.
		{"exact with no tokens", EmbeddedLLMTuningRequest{
			Context: &EmbeddedLLMContextTuningRequest{Mode: strPtr(config.EmbeddedLLMContextExact)},
		}},
		{"zero slots", EmbeddedLLMTuningRequest{Parallel: tuningIntPtr(0)}},
		{"negative slots", EmbeddedLLMTuningRequest{Parallel: tuningIntPtr(-2)}},
		{"a negative prompt-cache ceiling", EmbeddedLLMTuningRequest{CacheRAMMiB: tuningIntPtr(-1)}},
		{"a negative fit target", EmbeddedLLMTuningRequest{FitTargetMiB: tuningIntPtr(-1)}},
		{"a negative host reserve", EmbeddedLLMTuningRequest{HostReserveGiB: tuningF64Ptr(-0.5)}},
		{"a negative layer count", EmbeddedLLMTuningRequest{
			Offload: &EmbeddedLLMOffloadTuningRequest{
				Mode: strPtr(config.EmbeddedLLMOffloadLayers), Layers: tuningIntPtr(-1),
			},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, rec, _ := newEmbeddedTestAPI(t)
			f.config.EmbeddedLLM.Tuning = stored
			// A clean baseline on disk, so "the file did not change" is a real
			// observation rather than an assumption about the fixture.
			if err := config.Save(f.config, f.configPath); err != nil {
				t.Fatalf("saving the baseline config: %v", err)
			}
			before, err := readConfigBytes(t, f.configPath)
			if err != nil {
				t.Fatalf("reading the baseline config: %v", err)
			}

			err = f.SetEmbeddedLLMTuning(tc.req)
			if err == nil {
				t.Fatalf("the request %+v was accepted", tc.req)
			}
			if !strings.Contains(err.Error(), "embedded") {
				t.Errorf("the error %q does not say which section it refused", err)
			}

			if got := storedTuning(t, f); !reflect.DeepEqual(got, stored) {
				t.Errorf("the in-memory tuning changed to %+v after a refusal", got)
			}
			after, err := readConfigBytes(t, f.configPath)
			if err != nil {
				t.Fatalf("reading the config after the refusal: %v", err)
			}
			if before != after {
				t.Error("config.yaml changed even though the request was refused")
			}
			// The file must still be loadable — that is the whole point of
			// validating before writing.
			if _, err := config.Load(f.configPath); err != nil {
				t.Errorf("the config no longer loads after a refused write: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
			if n := rec.count(EventConfigUpdated); n != 0 {
				t.Errorf("config:updated was emitted %d time(s) for a refused write", n)
			}
		})
	}
}

// TestSetEmbeddedLLMTuningRollsBackWhenThePersistFails: a failed save must be
// indistinguishable from a refused one, so the in-memory section cannot be left
// claiming a tuning the file does not carry.
func TestSetEmbeddedLLMTuningRollsBackWhenThePersistFails(t *testing.T) {
	f, rec, mock := newEmbeddedTestAPI(t)
	f.config.EmbeddedLLM.Tuning = config.TuningConfig{Parallel: tuningIntPtr(1)}
	f.configPath = f.agentDir + "/does-not-exist/nested/config.yaml"

	err := f.SetEmbeddedLLMTuning(EmbeddedLLMTuningRequest{Parallel: tuningIntPtr(4)})
	if err == nil {
		t.Fatal("a write to an unwritable path was reported as a success")
	}
	if !strings.Contains(err.Error(), "persist") {
		t.Errorf("the error %q does not say the persist failed", err)
	}
	if got := storedTuning(t, f).Parallel; got == nil || *got != 1 {
		t.Errorf("the in-memory parallel = %v, want the rollback to 1", got)
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter ran %d time(s) after a failed persist, want 0", mock.rebuildRouterCalls)
	}
	time.Sleep(20 * time.Millisecond)
	if n := rec.count(EventConfigUpdated); n != 0 {
		t.Errorf("config:updated was emitted %d time(s) after a failed persist", n)
	}
}

// ── the setter: the shared write tail ──────────────────────────────────────

// TestSetEmbeddedLLMTuningEndsOnTheSharedConfigTail pins the delegation's
// requirement that a tuning write finishes the way every other embedded-LLM
// config mutation does: config:updated, then the judge/router rebuild.
func TestSetEmbeddedLLMTuningEndsOnTheSharedConfigTail(t *testing.T) {
	f, rec, mock := newEmbeddedTestAPI(t)

	if err := f.SetEmbeddedLLMTuning(EmbeddedLLMTuningRequest{Parallel: tuningIntPtr(2)}); err != nil {
		t.Fatalf("SetEmbeddedLLMTuning: %v", err)
	}

	rec.waitForCount(t, EventConfigUpdated, 1)
	if mock.rebuildJudgeCalls != 1 {
		t.Errorf("RebuildJudge ran %d time(s), want 1", mock.rebuildJudgeCalls)
	}
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter ran %d time(s), want 1", mock.rebuildRouterCalls)
	}
}

// TestSetEmbeddedLLMTuningThatChangesNothingWritesNothing is the no-op rule: a
// Save click with no edits must not rewrite config.yaml, emit config:updated or
// rebuild the router, because all three would announce a change that did not
// happen.
func TestSetEmbeddedLLMTuningThatChangesNothingWritesNothing(t *testing.T) {
	f, rec, mock := newEmbeddedTestAPI(t)
	f.config.EmbeddedLLM.Tuning = config.TuningConfig{Parallel: tuningIntPtr(2)}
	if err := config.Save(f.config, f.configPath); err != nil {
		t.Fatalf("saving the baseline config: %v", err)
	}
	before, err := readConfigBytes(t, f.configPath)
	if err != nil {
		t.Fatalf("reading the baseline config: %v", err)
	}

	// Setting a knob to the value it already holds, and setting an unset knob to
	// an explicit auto that resolves to the same plan, are both no-ops at the
	// SECTION level only in the first case — the second is a real change to the
	// persisted spelling, so it must still write. Only the first is asserted
	// here.
	if err := f.SetEmbeddedLLMTuning(EmbeddedLLMTuningRequest{Parallel: tuningIntPtr(2)}); err != nil {
		t.Fatalf("SetEmbeddedLLMTuning: %v", err)
	}

	after, err := readConfigBytes(t, f.configPath)
	if err != nil {
		t.Fatalf("reading the config after the no-op: %v", err)
	}
	if before != after {
		t.Error("config.yaml was rewritten for a patch that changed nothing")
	}
	time.Sleep(20 * time.Millisecond)
	if n := rec.count(EventConfigUpdated); n != 0 {
		t.Errorf("config:updated was emitted %d time(s) for a no-op patch", n)
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter ran %d time(s) for a no-op patch, want 0", mock.rebuildRouterCalls)
	}
}

// TestSetEmbeddedLLMTuningDoesNotRequireAnInstall: tuning is an operator SETTING,
// not install state — it survives a removal and applies to the next install, so
// refusing to write it before the model exists would lose the operator's choice
// at exactly the moment they are preparing to make one.
func TestSetEmbeddedLLMTuningDoesNotRequireAnInstall(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	if f.embeddedConfig().Installed {
		t.Fatal("the fixture starts installed; this test needs the not-installed state")
	}

	if err := f.SetEmbeddedLLMTuning(EmbeddedLLMTuningRequest{
		Offload: &EmbeddedLLMOffloadTuningRequest{Mode: strPtr(config.EmbeddedLLMOffloadCPU)},
	}); err != nil {
		t.Fatalf("SetEmbeddedLLMTuning while not installed: %v", err)
	}
	got := storedTuning(t, f)
	if got.Offload.Mode == nil || *got.Offload.Mode != config.EmbeddedLLMOffloadCPU {
		t.Errorf("offload.mode = %v, want %q", got.Offload.Mode, config.EmbeddedLLMOffloadCPU)
	}
	reloaded, err := config.Load(f.configPath)
	if err != nil {
		t.Fatalf("reloading the config: %v", err)
	}
	if reloaded.EmbeddedLLM.Installed {
		t.Error("writing the tuning marked the model installed")
	}
	if reloaded.EmbeddedLLM.Tuning.Offload.Mode == nil {
		t.Error("the tuning was not persisted")
	}
}

// TestApplyEmbeddedTuningRequestFoldsEveryKnob is the table half of the partial
// contract: the pure fold, one knob per case, checked against the stored section
// it started from.
func TestApplyEmbeddedTuningRequestFoldsEveryKnob(t *testing.T) {
	stored := config.TuningConfig{Parallel: tuningIntPtr(1)}

	cases := []struct {
		name string
		req  EmbeddedLLMTuningRequest
		want config.TuningConfig
	}{
		{"context replaces the whole knob", EmbeddedLLMTuningRequest{
			Context: &EmbeddedLLMContextTuningRequest{
				Mode: strPtr(config.EmbeddedLLMContextExact), Tokens: tuningIntPtr(16384),
			},
		}, config.TuningConfig{
			Parallel: tuningIntPtr(1),
			Context: config.EmbeddedLLMContextConfig{
				Mode: strPtr(config.EmbeddedLLMContextExact), Tokens: tuningIntPtr(16384),
			},
		}},
		{"an explicit auto is stored verbatim, not collapsed to unset", EmbeddedLLMTuningRequest{
			KVCacheType: strPtr(config.EmbeddedLLMTuningAuto),
		}, config.TuningConfig{Parallel: tuningIntPtr(1), KVCacheType: strPtr(config.EmbeddedLLMTuningAuto)}},
		{"offload replaces the whole knob", EmbeddedLLMTuningRequest{
			Offload: &EmbeddedLLMOffloadTuningRequest{Mode: strPtr(config.EmbeddedLLMOffloadAll)},
		}, config.TuningConfig{
			Parallel: tuningIntPtr(1),
			Offload:  config.EmbeddedLLMOffloadConfig{Mode: strPtr(config.EmbeddedLLMOffloadAll)},
		}},
		{"an explicit false is not a nil", EmbeddedLLMTuningRequest{Fit: tuningBoolPtr(false)},
			config.TuningConfig{Parallel: tuningIntPtr(1), Fit: tuningBoolPtr(false)}},
		{"an explicit zero cache ceiling is not a nil", EmbeddedLLMTuningRequest{CacheRAMMiB: tuningIntPtr(0)},
			config.TuningConfig{Parallel: tuningIntPtr(1), CacheRAMMiB: tuningIntPtr(0)}},
		{"the empty request keeps everything", EmbeddedLLMTuningRequest{}, stored},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := applyEmbeddedTuningRequest(stored, tc.req)
			if err != nil {
				t.Fatalf("applyEmbeddedTuningRequest: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("the folded section:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// TestApplyEmbeddedTuningRequestDoesNotAliasTheRequest pins the pointer copy on
// the WRITE side: a request the caller keeps holding must not be able to mutate
// the config it was folded into.
func TestApplyEmbeddedTuningRequestDoesNotAliasTheRequest(t *testing.T) {
	req := EmbeddedLLMTuningRequest{Parallel: tuningIntPtr(2), Fit: tuningBoolPtr(true)}
	got, err := applyEmbeddedTuningRequest(config.TuningConfig{}, req)
	if err != nil {
		t.Fatalf("applyEmbeddedTuningRequest: %v", err)
	}
	*req.Parallel = 99
	*req.Fit = false

	if *got.Parallel != 2 {
		t.Errorf("parallel = %d, want the value at fold time (2)", *got.Parallel)
	}
	if !*got.Fit {
		t.Error("fit = false, want the value at fold time (true)")
	}
}

// ── the status DTO additions ───────────────────────────────────────────────

// embeddedTopologyFixture is the reference machine's measured shape, quoted in
// core/embeddedllm/topology.go's header: one Metal device whose pool IS host RAM,
// beside a memoryless BLAS row that the parser already dropped.
func embeddedTopologyFixture() embeddedllm.MemoryTopology {
	const mib = int64(1) << 20
	return embeddedllm.MemoryTopology{
		Devices: []embeddedllm.DeviceMemory{
			{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: 110100, FreeMiB: 110100},
		},
		HostRAMGiB:        128,
		Unified:           true,
		DeviceBudgetBytes: 95000 * mib,
		HostBudgetBytes:   112000 * mib,
		ProbedAt:          "2026-09-25T12:00:00Z",
	}
}

// TestGetEmbeddedLLMStatusCarriesTheRecordedTopologyAndPlan is the read half of
// this delegation: the measured shape and the launch decision it informed are
// reachable from the status RPC, so a support bundle and the Settings page no
// longer have to open manifest.json to see why a plan looks the way it does.
func TestGetEmbeddedLLMStatusCarriesTheRecordedTopologyAndPlan(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	manifest := embeddedTestManifest(4321, 32768)
	topology := embeddedTopologyFixture()
	manifest.Topology = &topology
	manifest.Plan = &embeddedllm.MemoryPlan{
		Fit:               true,
		FitTargetMiB:      1024,
		FitMinContext:     embeddedllm.DefaultFitMinContext,
		ContextSize:       0,
		KVType:            embeddedllm.KVTypeF16,
		Packing:           embeddedllm.PackingPQ2_0,
		KVOffload:         true,
		MMProjOffload:     true,
		Parallel:          embeddedllm.DefaultParallel,
		GPUFamily:         embeddedllm.GPUFamilyAppleSilicon,
		DeviceBudgetMiB:   95000,
		HostBudgetMiB:     112000,
		ExpectedDeviceMiB: 24450,
		ExpectedHostMiB:   614,
		Notes:             []string{"the context was left to the runtime's own sizing pass"},
	}
	writeEmbeddedInstallTree(t, f.agentDir, manifest)
	f.Lifecycle().InitEmbeddedLLM()

	status := f.GetEmbeddedLLMStatus()

	// The topology summary.
	if len(status.Devices) != 1 {
		t.Fatalf("Devices = %+v, want the one recorded accelerator", status.Devices)
	}
	device := status.Devices[0]
	if device.Name != "MTL0" || device.Description != "Apple M4 Max" {
		t.Errorf("device = %+v, want MTL0 / Apple M4 Max", device)
	}
	if device.TotalMiB != 110100 || device.FreeMiB != 110100 {
		t.Errorf("device memory = %d/%d MiB, want 110100/110100", device.TotalMiB, device.FreeMiB)
	}
	if !status.Unified {
		t.Error("Unified = false, want the recorded unified pool")
	}
	if status.HostRAMGiB != 128 {
		t.Errorf("HostRAMGiB = %v, want 128", status.HostRAMGiB)
	}
	if status.DeviceBudgetMiB != 95000 || status.HostBudgetMiB != 112000 {
		t.Errorf("budgets = %d/%d MiB, want 95000/112000", status.DeviceBudgetMiB, status.HostBudgetMiB)
	}
	if status.TopologyProbedAt != "2026-09-25T12:00:00Z" {
		t.Errorf("TopologyProbedAt = %q", status.TopologyProbedAt)
	}

	// The effective plan.
	plan := status.Plan
	if !plan.Recorded {
		t.Fatal("Plan.Recorded = false, want the recorded plan")
	}
	if plan.Packing != string(embeddedllm.PackingPQ2_0) || plan.KVType != string(embeddedllm.KVTypeF16) {
		t.Errorf("plan packing/kv = %q/%q", plan.Packing, plan.KVType)
	}
	if !plan.Fit || plan.FitArg != "on" {
		t.Errorf("plan fit = %v (%q), want on", plan.Fit, plan.FitArg)
	}
	if plan.ContextSize != 0 {
		t.Errorf("plan ContextSize = %d under fit, want 0 (the runtime sizes it)", plan.ContextSize)
	}
	if plan.FitMinContext != embeddedllm.DefaultFitMinContext {
		t.Errorf("plan FitMinContext = %d, want the floor fit is held to", plan.FitMinContext)
	}
	if plan.OffloadMode != config.EmbeddedLLMTuningAuto {
		t.Errorf("plan OffloadMode = %q under fit, want %q", plan.OffloadMode, config.EmbeddedLLMTuningAuto)
	}
	if plan.Layers != -1 {
		t.Errorf("plan Layers = %d under fit, want the -1 omit sentinel", plan.Layers)
	}
	if plan.Parallel != embeddedllm.DefaultParallel {
		t.Errorf("plan Parallel = %d, want %d", plan.Parallel, embeddedllm.DefaultParallel)
	}
	if plan.ExpectedDeviceMiB != 24450 || plan.ExpectedHostMiB != 614 {
		t.Errorf("plan footprints = %d/%d MiB, want 24450/614", plan.ExpectedDeviceMiB, plan.ExpectedHostMiB)
	}
	if len(plan.Notes) != 1 || !strings.Contains(plan.Notes[0], "sizing pass") {
		t.Errorf("plan Notes = %v, want the recorded explanation verbatim", plan.Notes)
	}
	if plan.GPUFamily != string(embeddedllm.GPUFamilyAppleSilicon) {
		t.Errorf("plan GPUFamily = %q", plan.GPUFamily)
	}
}

// TestGetEmbeddedLLMStatusReportsAnUnrecordedTopologyAsUnknownNotZero is the
// fail-closed reading of a manifest with no topology and no plan — an install
// whose probe never answered, or one written before the fields existed. The zero
// values must not be reported as facts, because a zero device budget read as a
// measurement is exactly the refusal the combined memory gate exists to avoid.
func TestGetEmbeddedLLMStatusReportsAnUnrecordedTopologyAsUnknownNotZero(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 32768))
	f.Lifecycle().InitEmbeddedLLM()

	status := f.GetEmbeddedLLMStatus()

	if status.Devices == nil {
		t.Error("Devices is nil, want an empty array — the boundary never carries null")
	}
	if len(status.Devices) != 0 {
		t.Errorf("Devices = %+v, want none", status.Devices)
	}
	// TopologyProbedAt is the ONLY way to tell this empty array from a real
	// CPU-only answer, so it must be empty here.
	if status.TopologyProbedAt != "" {
		t.Errorf("TopologyProbedAt = %q, want it empty when no probe ever answered", status.TopologyProbedAt)
	}
	if status.Plan.Recorded {
		t.Error("Plan.Recorded = true for a manifest with no plan")
	}
	if status.Plan.Notes == nil {
		t.Error("Plan.Notes is nil, want an empty array")
	}
	if status.Plan.Packing != "" || status.Plan.KVType != "" {
		t.Errorf("an unrecorded plan reported packing=%q kv=%q, want both empty",
			status.Plan.Packing, status.Plan.KVType)
	}
	// The pre-existing install record still reads through, so this is a
	// degradation and not a broken status.
	if !status.Installed || status.Port != 4321 || status.ContextSize != 32768 {
		t.Errorf("status = %+v, want the install record intact", status)
	}
}

// TestEmbeddedPlanDTORendersTheOffloadDecision pins the derived offload mode,
// including the two sentinels a renderer would otherwise have to guess at: a nil
// Layers is -1 ("the flag is omitted"), and an explicit zero cache ceiling is 0
// ("the cache is disabled"), which are different facts.
func TestEmbeddedPlanDTORendersTheOffloadDecision(t *testing.T) {
	cpuOnly := 0
	partial := 24
	every := 99

	cases := []struct {
		name      string
		plan      *embeddedllm.MemoryPlan
		wantMode  string
		wantLayer int
		wantCache int
	}{
		{"nil plan", nil, config.EmbeddedLLMTuningAuto, -1, -1},
		{"under fit the runtime sizes it", &embeddedllm.MemoryPlan{Fit: true},
			config.EmbeddedLLMTuningAuto, -1, -1},
		{"no -ngl at all", &embeddedllm.MemoryPlan{}, config.EmbeddedLLMTuningAuto, -1, -1},
		{"nothing offloaded", &embeddedllm.MemoryPlan{Layers: &cpuOnly},
			config.EmbeddedLLMOffloadCPU, 0, -1},
		{"a partial offload", &embeddedllm.MemoryPlan{Layers: &partial},
			config.EmbeddedLLMOffloadLayers, 24, -1},
		// Core spells "every layer" with its own ceiling sentinel, which this
		// layer must not transcribe: an every-layer offload renders as a count.
		{"every layer is a count, not a label", &embeddedllm.MemoryPlan{Layers: &every},
			config.EmbeddedLLMOffloadLayers, 99, -1},
		{"a disabled prompt cache is 0, not omitted", &embeddedllm.MemoryPlan{CacheRAMMiB: tuningIntPtr(0)},
			config.EmbeddedLLMTuningAuto, -1, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := embeddedPlanDTO(tc.plan)
			if got.OffloadMode != tc.wantMode {
				t.Errorf("OffloadMode = %q, want %q", got.OffloadMode, tc.wantMode)
			}
			if got.Layers != tc.wantLayer {
				t.Errorf("Layers = %d, want %d", got.Layers, tc.wantLayer)
			}
			if got.CacheRAMMiB != tc.wantCache {
				t.Errorf("CacheRAMMiB = %d, want %d", got.CacheRAMMiB, tc.wantCache)
			}
			if got.Notes == nil {
				t.Error("Notes is nil, want an array — the boundary never carries null")
			}
			if got.Recorded != (tc.plan != nil) {
				t.Errorf("Recorded = %v, want %v", got.Recorded, tc.plan != nil)
			}
		})
	}
}

// TestEmbeddedLLMStatusAdditionsAreAdditiveOnTheWire is the guard-degradation
// criterion expressed on this side of the boundary: the payload still serializes
// with every pre-existing key present and correctly typed, the new composite
// fields never serialize as null, and nothing was renamed or retyped.
func TestEmbeddedLLMStatusAdditionsAreAdditiveOnTheWire(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	manifest := embeddedTestManifest(4321, 32768)
	topology := embeddedTopologyFixture()
	manifest.Topology = &topology
	manifest.Plan = &embeddedllm.MemoryPlan{Packing: embeddedllm.PackingPQ2_0, Parallel: 1}
	writeEmbeddedInstallTree(t, f.agentDir, manifest)
	f.Lifecycle().InitEmbeddedLLM()

	encoded, err := json.Marshal(f.GetEmbeddedLLMStatus())
	if err != nil {
		t.Fatalf("marshalling the status: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshalling the status: %v", err)
	}

	// The 21 fields the existing frontend guard checks, with the type each
	// typeof-branch expects. A rename or a retype here is what would break an
	// older UI, so they are pinned explicitly.
	older := map[string]string{
		"state": "string", "installed": "boolean", "installing": "boolean",
		"loading": "boolean", "loaded": "boolean", "packing": "string",
		"backend": "string", "port": "number", "context_size": "number",
		"auto_unload_enabled": "boolean", "auto_unload_minutes": "number",
		"idle_remaining_seconds": "number", "base_url": "string",
		"model_id": "string", "model_name": "string", "runtime_version": "string",
		"installed_at": "string", "model_file": "string", "pid": "number",
		"error": "string", "available": "boolean",
	}
	for key, want := range older {
		value, ok := raw[key]
		if !ok {
			t.Errorf("the pre-existing field %q is missing from the payload", key)
			continue
		}
		if got := jsonKind(value); got != want {
			t.Errorf("the pre-existing field %q is a %s, want %s", key, got, want)
		}
	}

	// The additions.
	additions := map[string]string{
		"devices": "array", "unified": "boolean", "host_ram_gib": "number",
		"device_budget_mib": "number", "host_budget_mib": "number",
		"topology_probed_at": "string", "plan": "object",
		"reload_required": "boolean",
	}
	for key, want := range additions {
		value, ok := raw[key]
		if !ok {
			t.Errorf("the new field %q is missing from the payload", key)
			continue
		}
		if got := jsonKind(value); got != want {
			t.Errorf("the new field %q is a %s, want %s", key, got, want)
		}
	}

	// fit_warning is the one addition allowed to be ABSENT: omitempty keeps a
	// healthy status free of it, so the additive contract here is "absent or a
	// string", never null and never another type.
	if value, ok := raw["fit_warning"]; ok {
		if got := jsonKind(value); got != "string" {
			t.Errorf("fit_warning is a %s, want a string", got)
		}
	}

	// A null array is the one shape the "always present" contract forbids, so
	// check it on the unpopulated payload too.
	var plan struct {
		Notes    []string `json:"notes"`
		Recorded bool     `json:"recorded"`
	}
	if err := json.Unmarshal(raw["plan"], &plan); err != nil {
		t.Fatalf("unmarshalling plan: %v", err)
	}
	if plan.Notes == nil {
		t.Error("plan.notes serialized as null, want an array")
	}

	empty, err := json.Marshal(EmbeddedLLMStatus{Devices: []EmbeddedLLMDevice{}, Plan: embeddedPlanDTO(nil)})
	if err != nil {
		t.Fatalf("marshalling an empty status: %v", err)
	}
	if strings.Contains(string(empty), `"devices":null`) || strings.Contains(string(empty), `"notes":null`) {
		t.Errorf("an unpopulated status serialized a null array: %s", empty)
	}
}

// jsonKind reports the JSON typeof of a raw value, using the same vocabulary as
// the frontend guard's typeof checks.
func jsonKind(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	switch {
	case text == "null":
		return "null"
	case strings.HasPrefix(text, "\""):
		return "string"
	case strings.HasPrefix(text, "["):
		return "array"
	case strings.HasPrefix(text, "{"):
		return "object"
	case text == "true" || text == "false":
		return "boolean"
	default:
		return "number"
	}
}

// ── reload_required ────────────────────────────────────────────────────────

// residentEmbeddedModel drives a real load through the faked spawn so the model
// is resident, and returns the supervisor for the teardown assertions.
func residentEmbeddedModel(t *testing.T, f *FrontendAPI) {
	t.Helper()
	_, port := modelsEndpoint(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(port, 32768))
	stubFreePortProbe(t, f)
	proc := newFakeEmbeddedProcess(4242)
	f.embedded.spawnFn = func(context.Context, embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		return proc, nil
	}
	tightenEmbeddedBudgets(t, f)
	f.Lifecycle().InitEmbeddedLLM()
	if err := f.LoadEmbeddedLLM(); err != nil {
		t.Fatalf("LoadEmbeddedLLM: %v", err)
	}
	if status := f.GetEmbeddedLLMStatus(); !status.Loaded {
		t.Fatalf("the fixture model is not resident: %+v", status)
	}
}

// TestSetEmbeddedLLMTuningReportsReloadRequiredInsteadOfRestarting pins the
// resident-model decision: the write is persisted, the running process is left
// ALONE, and the status says a reload is needed. A restart would be a
// multi-minute denial of service with no confirmation behind it.
func TestSetEmbeddedLLMTuningReportsReloadRequiredInsteadOfRestarting(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	residentEmbeddedModel(t, f)

	before := f.GetEmbeddedLLMStatus()
	if before.ReloadRequired {
		t.Fatal("reload_required is already true before any tuning change")
	}
	pidBefore := before.Pid

	if err := f.SetEmbeddedLLMTuning(EmbeddedLLMTuningRequest{Parallel: tuningIntPtr(2)}); err != nil {
		t.Fatalf("SetEmbeddedLLMTuning: %v", err)
	}

	after := f.GetEmbeddedLLMStatus()
	if !after.ReloadRequired {
		t.Error("reload_required = false after a tuning change to a resident model")
	}
	// The decision this pins: the process was NOT restarted to apply it.
	if !after.Loaded {
		t.Errorf("status = %+v, want the model still resident", after)
	}
	if after.Pid != pidBefore {
		t.Errorf("the pid changed from %d to %d — the resident model was restarted", pidBefore, after.Pid)
	}

	// An unload clears the flag: there is no resident process to be out of date.
	if err := f.UnloadEmbeddedLLM(); err != nil {
		t.Fatalf("UnloadEmbeddedLLM: %v", err)
	}
	if status := f.GetEmbeddedLLMStatus(); status.ReloadRequired {
		t.Errorf("reload_required = %v after an unload, want false", status.ReloadRequired)
	}

	// And the NEXT load launches with the stored tuning, so the flag goes away
	// on its own once the operator does reload.
	if err := f.LoadEmbeddedLLM(); err != nil {
		t.Fatalf("a second LoadEmbeddedLLM: %v", err)
	}
	if status := f.GetEmbeddedLLMStatus(); status.ReloadRequired {
		t.Error("reload_required is still true after a load that used the stored tuning")
	}
}

// TestEmbeddedTuningReloadRequiredIsNotRaisedByAnEquivalentRespelling pins the
// fingerprint's choice of vocabulary: two spellings that resolve to the SAME
// launch shape must not raise a demand to reload, or the badge would fire on a
// Save that changed nothing that matters.
func TestEmbeddedTuningReloadRequiredIsNotRaisedByAnEquivalentRespelling(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	residentEmbeddedModel(t, f)

	// An absent kv_cache_type and an explicit "auto" resolve to the same
	// adaptive escalation, so the plan is unchanged even though the section is
	// not.
	if err := f.SetEmbeddedLLMTuning(EmbeddedLLMTuningRequest{
		KVCacheType: strPtr(config.EmbeddedLLMTuningAuto),
	}); err != nil {
		t.Fatalf("SetEmbeddedLLMTuning: %v", err)
	}
	if status := f.GetEmbeddedLLMStatus(); status.ReloadRequired {
		t.Error("reload_required = true for a respelling that resolves to the same plan")
	}

	// A real change still raises it.
	if err := f.SetEmbeddedLLMTuning(EmbeddedLLMTuningRequest{KVCacheType: strPtr("q8_0")}); err != nil {
		t.Fatalf("SetEmbeddedLLMTuning: %v", err)
	}
	if status := f.GetEmbeddedLLMStatus(); !status.ReloadRequired {
		t.Error("reload_required = false after a change to the KV precision")
	}
}

// TestEmbeddedTuningReloadRequiredIsFalseWhenNothingIsResident: the flag is
// about a running process, so an unloaded model never carries it — even with a
// tuning that has never been launched.
func TestEmbeddedTuningReloadRequiredIsFalseWhenNothingIsResident(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	if err := f.SetEmbeddedLLMTuning(EmbeddedLLMTuningRequest{Parallel: tuningIntPtr(3)}); err != nil {
		t.Fatalf("SetEmbeddedLLMTuning: %v", err)
	}
	if status := f.GetEmbeddedLLMStatus(); status.ReloadRequired {
		t.Errorf("reload_required = %v with no resident model, want false", status.ReloadRequired)
	}
}

// ── the on-demand probe ────────────────────────────────────────────────────

// TestProbeEmbeddedLLMDevicesReturnsTheMeasuredTopology is the read path: an
// explicit re-probe answers with this instant's inventory, and does NOT persist
// it — the install record keeps the snapshot the plan was actually made from.
func TestProbeEmbeddedLLMDevicesReturnsTheMeasuredTopology(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	layout, manifest := writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 32768))
	f.Lifecycle().InitEmbeddedLLM()

	var gotBinary string
	f.embedded.deviceProbeFn = func(_ context.Context, binaryPath string, _ *slog.Logger) (embeddedllm.MemoryTopology, bool) {
		gotBinary = binaryPath
		return embeddedTopologyFixture(), true
	}

	dto, err := f.ProbeEmbeddedLLMDevices()
	if err != nil {
		t.Fatalf("ProbeEmbeddedLLMDevices: %v", err)
	}

	if len(dto.Devices) != 1 || dto.Devices[0].Name != "MTL0" {
		t.Errorf("Devices = %+v, want the measured MTL0", dto.Devices)
	}
	if !dto.Unified || dto.HostRAMGiB != 128 {
		t.Errorf("dto = %+v, want a unified 128 GiB pool", dto)
	}
	if dto.DeviceBudgetMiB != 95000 || dto.HostBudgetMiB != 112000 {
		t.Errorf("budgets = %d/%d MiB, want 95000/112000", dto.DeviceBudgetMiB, dto.HostBudgetMiB)
	}
	if dto.ProbedAt != "2026-09-25T12:00:00Z" {
		t.Errorf("ProbedAt = %q", dto.ProbedAt)
	}

	// The probe asked the PROVISIONED binary, not a path this layer invented.
	runtimeDir, err := layout.RuntimeDir(manifest.Backend)
	if err != nil {
		t.Fatalf("RuntimeDir: %v", err)
	}
	want, err := embeddedllm.ServerBinaryPath(runtimeDir, "")
	if err != nil {
		t.Fatalf("ServerBinaryPath: %v", err)
	}
	if gotBinary != want {
		t.Errorf("the probe was aimed at %q, want %q", gotBinary, want)
	}

	// Nothing was persisted: the status still reports no recorded topology.
	if status := f.GetEmbeddedLLMStatus(); len(status.Devices) != 0 || status.TopologyProbedAt != "" {
		t.Errorf("status = %+v, want the install record untouched by an on-demand probe", status)
	}
}

// TestProbeEmbeddedLLMDevicesTurnsAnUnansweredProbeIntoAnError is the difference
// between this RPC and the load path. Core's probe is fail-soft by contract — a
// load must not fail because a driver query wedged — but here nothing depends on
// a load and everything depends on the answer being real, so (zero, false) must
// surface as a refusal rather than as a machine with no accelerator.
func TestProbeEmbeddedLLMDevicesTurnsAnUnansweredProbeIntoAnError(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 32768))
	f.Lifecycle().InitEmbeddedLLM()
	f.embedded.deviceProbeFn = func(context.Context, string, *slog.Logger) (embeddedllm.MemoryTopology, bool) {
		return embeddedllm.MemoryTopology{}, false
	}

	dto, err := f.ProbeEmbeddedLLMDevices()
	if err == nil {
		t.Fatalf("ProbeEmbeddedLLMDevices = %+v, want an error for a probe that did not answer", dto)
	}
	if !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("the error %q does not say the probe did not answer", err)
	}
	if dto.Devices != nil {
		t.Errorf("Devices = %+v beside an error, want no partial answer", dto.Devices)
	}
}

// TestProbeEmbeddedLLMDevicesRefusesWhenNotInstalled: the probe asks the
// provisioned runtime, so there is nothing to ask before an install. Guessing
// from the OS instead would report the hardware rather than what this build was
// compiled to use.
func TestProbeEmbeddedLLMDevicesRefusesWhenNotInstalled(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	f.embedded.deviceProbeFn = func(context.Context, string, *slog.Logger) (embeddedllm.MemoryTopology, bool) {
		t.Error("the device probe ran with nothing installed")
		return embeddedllm.MemoryTopology{}, false
	}

	if _, err := f.ProbeEmbeddedLLMDevices(); err == nil {
		t.Fatal("ProbeEmbeddedLLMDevices succeeded with nothing installed")
	} else if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("the error %q does not say the model is not installed", err)
	}
}

// TestProbeEmbeddedLLMDevicesRefusesWhileAnInstallRuns: a half-staged runtime
// tree has no trustworthy binary to ask.
func TestProbeEmbeddedLLMDevicesRefusesWhileAnInstallRuns(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 32768))
	f.Lifecycle().InitEmbeddedLLM()
	f.embedded.deviceProbeFn = func(context.Context, string, *slog.Logger) (embeddedllm.MemoryTopology, bool) {
		t.Error("the device probe ran during an install")
		return embeddedllm.MemoryTopology{}, false
	}
	if !f.beginEmbeddedInstall() {
		t.Fatal("the single-run gate refused the first install")
	}
	defer f.endEmbeddedInstall()

	if _, err := f.ProbeEmbeddedLLMDevices(); err == nil {
		t.Fatal("ProbeEmbeddedLLMDevices succeeded during an install")
	} else if !strings.Contains(err.Error(), "install is running") {
		t.Errorf("the error %q does not say an install is running", err)
	}
}

// TestProbeEmbeddedLLMDevicesBoundsTheSpawn keeps the RPC inside the documented
// budget for a synchronous gate: an explicit probe is a Settings click, so a
// wedged driver query must return an actionable error rather than hang the
// dialog. Core bounds each command at 2s; this is the outer belt.
func TestProbeEmbeddedLLMDevicesBoundsTheSpawn(t *testing.T) {
	if embeddedProbeTimeout > 30*time.Second {
		t.Errorf("embeddedProbeTimeout = %v, want the documented 30s ceiling", embeddedProbeTimeout)
	}

	f, _, _ := newEmbeddedTestAPI(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 32768))
	f.Lifecycle().InitEmbeddedLLM()
	f.embedded.deviceProbeFn = func(ctx context.Context, _ string, _ *slog.Logger) (embeddedllm.MemoryTopology, bool) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("the probe was handed an unbounded context")
			return embeddedllm.MemoryTopology{}, false
		}
		if left := time.Until(deadline); left <= 0 || left > embeddedProbeTimeout {
			t.Errorf("the probe context has %v left, want a fresh budget of at most %v", left, embeddedProbeTimeout)
		}
		return embeddedTopologyFixture(), true
	}

	if _, err := f.ProbeEmbeddedLLMDevices(); err != nil {
		t.Fatalf("ProbeEmbeddedLLMDevices: %v", err)
	}
}

// readConfigBytes returns the raw config.yaml so a test can assert the FILE did
// not change, not merely that the parsed section did not.
func readConfigBytes(t *testing.T, path string) (string, error) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// TestEmbeddedDevicesDTONeverCarriesANullArray pins the always-an-array rule on
// the probe's own DTO, which the status additions share.
func TestEmbeddedDevicesDTONeverCarriesANullArray(t *testing.T) {
	dto := embeddedDevicesDTO(embeddedllm.MemoryTopology{})
	if dto.Devices == nil {
		t.Fatal("Devices is nil for an empty topology, want an empty array")
	}
	encoded, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if strings.Contains(string(encoded), `"devices":null`) {
		t.Errorf("an empty topology serialized a null array: %s", encoded)
	}
}

// TestEmbeddedTuningFingerprintIsStableForEquivalentSections is the property
// reload_required rests on: the fingerprint is of the TRANSLATED plan, so a
// section that resolves to the same launch shape fingerprints the same.
func TestEmbeddedTuningFingerprintIsStableForEquivalentSections(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)

	f.config.EmbeddedLLM.Tuning = config.TuningConfig{}
	unset := f.embeddedTuningFingerprint()

	f.config.EmbeddedLLM.Tuning = config.TuningConfig{KVCacheType: strPtr(config.EmbeddedLLMTuningAuto)}
	explicit := f.embeddedTuningFingerprint()

	if unset != explicit {
		t.Errorf("an unset kv_cache_type fingerprints %s but an explicit auto fingerprints %s", unset, explicit)
	}

	f.config.EmbeddedLLM.Tuning = config.TuningConfig{KVCacheType: strPtr("q8_0")}
	if changed := f.embeddedTuningFingerprint(); changed == unset {
		t.Error("a real change did not move the fingerprint")
	}
	if unset == "" {
		t.Error("the fingerprint is empty, which would make every comparison equal")
	}
}

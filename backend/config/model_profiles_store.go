package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/v0lka/sp4rk/safeio"
)

// Persistence layer for custom ModelProfiles profiles: the versioned YAML
// store at ~/.c0wrk/model-profiles.yaml holding the operator-authored
// profiles beside the compiled-in predefined catalog. Format, fail-soft
// load, and fail-closed atomic save are specified in
// specs/domains/model-profiles.md; the pre-write guard is ensureStoreWritable.

// modelProfilesFormatVersion is the only file format version this build
// understands; any other version is uninterpretable — load degrades to an
// empty list with a warning, save refuses to overwrite (see
// ensureStoreWritable) so a newer build can still read the file.
const modelProfilesFormatVersion = 1

// modelProfileNameMaxLength bounds a custom profile display name, measured in
// runes (not bytes) so a multibyte name gets the full budget. The limit
// keeps generated ids sane and UI lists readable; predefined names are all
// far below it.
const modelProfileNameMaxLength = 64

// modelProfilesFile is the on-disk document.
type modelProfilesFile struct {
	Version  int            `yaml:"version"`
	Profiles []ModelProfile `yaml:"profiles"`
}

// LoadCustomModelProfiles reads the custom profiles from path, fail-soft and
// never an error (specs/domains/model-profiles.md): a missing file is the
// pristine state, and any brokenness drops the offending entries, each
// reported as a warning string for the UI. The returned profiles are
// validated clones safe to hand out.
func LoadCustomModelProfiles(path string) (profiles []ModelProfile, warnings []string) {
	data, err := safeio.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Pristine state: no file means no custom profiles, no warning.
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("model profiles: cannot read %s: %v (using no custom profiles)", path, err)}
	}

	var file modelProfilesFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, []string{fmt.Sprintf("model profiles: %s is not valid YAML: %v (using no custom profiles)", path, err)}
	}
	if file.Version != modelProfilesFormatVersion {
		return nil, []string{fmt.Sprintf("model profiles: %s has unsupported format version %d (expected %d); using no custom profiles", path, file.Version, modelProfilesFormatVersion)}
	}

	predefined := PredefinedModelProfiles()
	seenIDs := make(map[string]struct{}, len(predefined)+len(file.Profiles))
	seenNames := make(map[string]struct{}, len(predefined)+len(file.Profiles))
	for _, p := range predefined {
		seenIDs[p.ID] = struct{}{}
		seenNames[p.Name] = struct{}{}
	}

	profiles = make([]ModelProfile, 0, len(file.Profiles))
	for i, raw := range file.Profiles {
		validated, err := validateCustomModelProfile(raw, seenIDs, seenNames)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("model profiles: entry %d skipped: %v", i+1, err))
			continue
		}
		profiles = append(profiles, validated)
	}
	return profiles, warnings
}

// ensureStoreWritable is the pre-write guard for SaveCustomModelProfiles: a
// full-set rewrite over a file this build cannot fully interpret would
// silently destroy the bytes it cannot represent. Refuses (before any temp
// file is created) when the file is not valid YAML, carries a foreign format
// version, or holds an entry the current validator rejects — a fail-soft
// load would drop it and the rewrite would erase it. A missing (or empty)
// file passes. The check reads only the on-disk file, never the set being
// saved.
func ensureStoreWritable(path string) error {
	data, err := safeio.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("model profiles: cannot read %s before save: %w", path, err)
	}
	var file modelProfilesFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("model profiles: %s is not valid YAML; refusing to overwrite it (fix or remove it first): %w", path, err)
	}
	// An empty (or whitespace-only) file holds no data to preserve: treat it as
	// pristine so an externally-created empty file cannot wedge saves.
	if file.Version == 0 && len(file.Profiles) == 0 && len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if file.Version != modelProfilesFormatVersion {
		return fmt.Errorf("model profiles: %s has unsupported format version %d (expected %d); refusing to overwrite it", path, file.Version, modelProfilesFormatVersion)
	}
	// Re-validate the existing entries against a fresh namespace so an entry a
	// fail-soft load would have skipped blocks the write instead of being
	// silently pruned by it.
	predefined := PredefinedModelProfiles()
	seenIDs := make(map[string]struct{}, len(predefined)+len(file.Profiles))
	seenNames := make(map[string]struct{}, len(predefined)+len(file.Profiles))
	for _, p := range predefined {
		seenIDs[p.ID] = struct{}{}
		seenNames[p.Name] = struct{}{}
	}
	for i, raw := range file.Profiles {
		if _, err := validateCustomModelProfile(raw, seenIDs, seenNames); err != nil {
			return fmt.Errorf("model profiles: %s entry %d is invalid (fix or back up the file before saving): %w", path, i+1, err)
		}
	}
	return nil
}

// SaveCustomModelProfiles validates the entire set and writes it to path as
// an atomic full rewrite (tmp + rename). Saving is fail-closed, unlike
// loading: an invalid entry or collision aborts with the file untouched, and
// an uninterpretable pre-existing file is refused outright via
// ensureStoreWritable rather than silently pruned
// (specs/domains/model-profiles.md). An empty set is legal; the file is
// created lazily here.
func SaveCustomModelProfiles(path string, profiles []ModelProfile) error {
	if err := ensureStoreWritable(path); err != nil {
		return err
	}
	predefined := PredefinedModelProfiles()
	seenIDs := make(map[string]struct{}, len(predefined)+len(profiles))
	seenNames := make(map[string]struct{}, len(predefined)+len(profiles))
	for _, p := range predefined {
		seenIDs[p.ID] = struct{}{}
		seenNames[p.Name] = struct{}{}
	}

	validated := make([]ModelProfile, 0, len(profiles))
	for i, raw := range profiles {
		v, err := validateCustomModelProfile(raw, seenIDs, seenNames)
		if err != nil {
			return fmt.Errorf("model profiles: entry %d is invalid, refusing to save: %w", i+1, err)
		}
		validated = append(validated, v)
	}

	data, err := yaml.Marshal(&modelProfilesFile{
		Version:  modelProfilesFormatVersion,
		Profiles: validated,
	})
	if err != nil {
		return fmt.Errorf("model profiles: failed to marshal: %w", err)
	}

	// Write atomically via safeio: a randomized temp name (no plantable fixed
	// "<path>.tmp" — a planted FIFO blocked the write-open forever, a planted
	// symlink was followed into an arbitrary write target), and a final
	// rename that replaces a planted symlink at path instead of writing
	// through it. The mode is an unconditional 0o600, matching Save:
	// WriteFileAtomic publishes the staging file with an exact fchmod, so a
	// literal 0o644 would silently widen the file next to the
	// secrets-bearing config.yaml.
	if err := safeio.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("model profiles: failed to write file: %w", err)
	}
	return nil
}

// CreateCustomModelProfile builds a new custom profile for name+cfg with a
// generated id. The name must be unique against the predefined catalog and
// the stored custom set (existing); the id is a deterministic slug of the
// name (generateCustomModelProfileID), deduplicated against every known id —
// written to the file once and never regenerated. Values are validated by
// NewModelProfile.
func CreateCustomModelProfile(name string, cfg ModelProfileConfig, existing []ModelProfile) (ModelProfile, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ModelProfile{}, errors.New("model profile name must not be empty")
	}
	if n := utf8.RuneCountInString(name); n > modelProfileNameMaxLength {
		return ModelProfile{}, fmt.Errorf("model profile name must be at most %d characters, got %d", modelProfileNameMaxLength, n)
	}
	for _, p := range PredefinedModelProfiles() {
		if p.Name == name {
			return ModelProfile{}, fmt.Errorf("model profile name %q collides with the predefined profile %q", name, p.ID)
		}
	}
	for _, p := range existing {
		if p.Name == name {
			return ModelProfile{}, fmt.Errorf("model profile name %q is already used by custom profile %q", name, p.ID)
		}
	}

	taken := make(map[string]struct{}, len(existing)+len(predefinedModelProfiles))
	for _, p := range PredefinedModelProfiles() {
		taken[p.ID] = struct{}{}
	}
	for _, p := range existing {
		taken[p.ID] = struct{}{}
	}
	id := generateCustomModelProfileID(name, taken)
	return NewModelProfile(id, name, ModelProfileKindCustom, cfg)
}

// DeleteCustomModelProfile removes the stored custom profile with the given
// id from the file at path and persists the remainder atomically, returning
// it. An unstored id (including any predefined id — the file only ever holds
// customs) is an error and leaves the file untouched. Fail-soft load
// warnings are irrelevant here: entries a load would discard are already
// absent from the stored set.
func DeleteCustomModelProfile(path, id string) ([]ModelProfile, error) {
	profiles, _ := LoadCustomModelProfiles(path)
	remaining := make([]ModelProfile, 0, len(profiles))
	found := false
	for _, p := range profiles {
		if p.ID == id {
			found = true
			continue
		}
		remaining = append(remaining, p)
	}
	if !found {
		return nil, fmt.Errorf("model profile %q is not a stored custom profile", id)
	}
	if err := SaveCustomModelProfiles(path, remaining); err != nil {
		return nil, err
	}
	return remaining, nil
}

// validateCustomModelProfile is the shared load/save gate for one stored
// profile: everything NewModelProfile enforces plus the store-level rules —
// kind=custom only (the file never stores predefined entries), the
// modelProfileNameMaxLength bound, and no id/name collision with the
// seenIDs/seenNames namespaces the callers pre-seed with the predefined
// catalog. On success the entry is registered in both maps and a validated,
// defensively cloned copy is returned.
func validateCustomModelProfile(raw ModelProfile, seenIDs, seenNames map[string]struct{}) (ModelProfile, error) {
	if raw.Kind != ModelProfileKindCustom {
		return ModelProfile{}, fmt.Errorf("profile %q has kind %q but the profile file only stores custom profiles", raw.ID, raw.Kind)
	}
	name := strings.TrimSpace(raw.Name)
	if n := utf8.RuneCountInString(name); n > modelProfileNameMaxLength {
		return ModelProfile{}, fmt.Errorf("profile %q: name must be at most %d characters, got %d", raw.ID, modelProfileNameMaxLength, n)
	}
	validated, err := NewModelProfile(raw.ID, raw.Name, raw.Kind, raw.Config)
	if err != nil {
		return ModelProfile{}, err
	}
	// Canonicalize an empty always_present list to nil: YAML renders a nil
	// slice as [] and parses it back as a non-nil empty slice, so without
	// this normalization a nil-vs-empty difference would make save→load
	// round-trips fail DeepEqual and flip-flop the file content.
	if len(validated.Config.EssentialTools.AlwaysPresent) == 0 {
		validated.Config.EssentialTools.AlwaysPresent = nil
	}
	if _, dup := seenIDs[validated.ID]; dup {
		return ModelProfile{}, fmt.Errorf("duplicate model profile id %q", validated.ID)
	}
	if _, dup := seenNames[validated.Name]; dup {
		return ModelProfile{}, fmt.Errorf("model profile name %q collides with a predefined or earlier profile", validated.Name)
	}
	seenIDs[validated.ID] = struct{}{}
	seenNames[validated.Name] = struct{}{}
	return validated, nil
}

// modelProfileNameSlugSeparators matches every character that is not a
// lowercase ASCII alphanum; runs of them become single '-' separators when
// turning a display name into an id slug.
var modelProfileNameSlugSeparators = regexp.MustCompile(`[^a-z0-9]+`)

// generateCustomModelProfileID derives a fresh profile id from a display
// name: lowercased, every non-[a-z0-9] run collapsed to a single '-'
// separator, trimmed ("Феникс" → "" → the "profile" fallback). A taken slug
// gets "-2", "-3", … suffixes; past the sanity cap the suffix goes
// time-based, so generation always terminates unique and pattern-valid.
func generateCustomModelProfileID(name string, taken map[string]struct{}) string {
	base := strings.Trim(modelProfileNameSlugSeparators.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if base == "" {
		base = "profile"
	}
	if _, exists := taken[base]; !exists {
		return base
	}
	for i := 2; i <= 10000; i++ {
		candidate := base + "-" + strconv.Itoa(i)
		if _, exists := taken[candidate]; !exists {
			return candidate
		}
	}
	// Pathological fallback (more than 10000 colliding slugs): a
	// time-based suffix keeps the id unique and pattern-valid.
	return fmt.Sprintf("%s-t%d", base, time.Now().UnixNano())
}

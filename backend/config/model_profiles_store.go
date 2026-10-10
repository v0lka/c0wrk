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

// Persistence layer for custom ModelProfiles profiles: a versioned YAML file at
// ~/.c0wrk/model-profiles.yaml (path from ModelProfilesPath) holding the
// operator-authored profiles alongside the hard-coded predefined catalog
// from modelProfiles_profiles.go.
//
// Format (version 1):
//
//	version: 1
//	profiles:
//	  - id: my-tuned-profile
//	    name: My Tuned Profile
//	    kind: custom
//	    config: { ...the 25 profile knobs... }
//
// Loading is fail-soft by design: a fully broken file (unreadable,
// unparseable, wrong version) degrades to an empty profile list with a
// warning instead of failing startup, and a partially broken file keeps
// its valid entries while the broken ones are discarded with per-entry
// warnings. Saving is fail-closed: the whole set is validated before any
// byte is written, and the write itself is an atomic tmp+rename full
// rewrite (mirroring Save), so a half-written file can never exist.
//
// A full rewrite re-emits only the set it is handed, so — to avoid
// destroying content a fail-soft load would have hidden — the writer first
// refuses to overwrite a file it cannot fully interpret (foreign format
// version, unparseable YAML, or an entry the current validator rejects);
// see ensureStoreWritable. Real save errors leave the on-disk file
// untouched.

// modelProfilesFormatVersion is the only file format version this build
// understands. A file with any other version value is treated as
// uninterpretable: load degrades to an empty list with a warning, and save
// refuses to overwrite it (see ensureStoreWritable) so the file content is
// left untouched for a newer build to read.
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

// LoadCustomModelProfiles reads the custom profiles from path (fail-soft).
// It never returns an error: a missing file is the pristine "no custom
// profiles" state, and any brokenness — unreadable file, YAML parse
// failure, unsupported version, invalid entry, id/name collision with the
// predefined catalog or within the file — degrades to dropping the
// offending entries and reporting each drop as a warning string. The
// caller surfaces warnings in the UI; the returned profiles are validated
// clones safe to hand out.
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

// ensureStoreWritable is the pre-write guard for SaveCustomModelProfiles. A
// save is a full-set rewrite, so writing over a file this build cannot fully
// interpret would silently destroy the bytes it cannot represent. The guard
// reads the existing file and refuses (before any temp file is created) when:
//
//   - the file is not valid YAML — uninterpretable, do not clobber;
//   - the file carries a format version other than the current one — a newer
//     build's document (forward-compat data loss; the version note above);
//   - the file holds an entry the current validator rejects — a fail-soft load
//     would drop it, and a full rewrite would then erase it (the pruning
//     hazard).
//
// A missing file is the pristine "nothing to preserve" state and passes. The
// check reads only the on-disk file; it never inspects the set being saved.
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
// an atomic full rewrite (write to a .tmp sibling, then rename — mirroring
// Save for config.yaml). Unlike loading, saving is fail-closed: any
// invalid entry or id/name collision (with the predefined catalog or
// within the set) aborts the save with an error and leaves the file
// untouched, and a pre-existing file this build cannot fully interpret is
// refused outright (see ensureStoreWritable) rather than silently pruned by
// the full rewrite. Saving an empty set is legal and writes a version marker
// with an empty list; the file is created lazily here if it does not exist yet.
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

	// Write atomically via safeio: the temporary file carries a randomized
	// name (no plantable fixed "<path>.tmp" — a FIFO planted there blocked
	// the write-open forever, and a symlink was followed into an arbitrary
	// write target), and the final rename replaces a planted symlink at path
	// itself instead of writing through it (same pattern as Save).
	//
	// The mode is an unconditional 0o600, matching Save: WriteFileAtomic
	// publishes the staging file with an exact fchmod and no umask
	// filtering, so a literal 0o644 would silently widen the file on hosts
	// whose umask used to keep it tighter. The store lives next to the
	// secrets-bearing config.yaml and gets the same owner-only hardening.
	if err := safeio.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("model profiles: failed to write file: %w", err)
	}
	return nil
}

// CreateCustomModelProfile builds a new custom profile for name+cfg with a
// generated id. The name must be unique against the predefined catalog
// names and the existing custom profile names (existing is the currently
// stored custom set, e.g. from LoadCustomModelProfiles); the id is derived
// deterministically from the name (lowercase slug, see
// generateCustomModelProfileID) and deduplicated against every predefined
// and existing custom id, so it is stable across save→load round-trips —
// the id is written to the file once and never regenerated. Values are
// validated by NewModelProfile.
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

// DeleteCustomModelProfile removes the custom profile with the given id from
// the file at path and persists the remainder atomically. It returns the
// remaining profiles. Deleting an id that is not stored (including any
// predefined id — the file only ever holds customs) is an error and leaves
// the file untouched. Warnings from the internal fail-soft load are
// irrelevant here: entries a load would discard are already absent from
// the stored set this function operates on.
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
// profile. It enforces everything NewModelProfile enforces (id slug, name,
// kind, the 25 knob values) plus the store-level rules: the entry must be
// kind=custom (the file never stores predefined entries), the name must
// respect modelProfileNameMaxLength, and the id/name must not collide with
// the ids/names registered in seenIDs/seenNames (pre-seeded with the
// predefined catalog by the callers). On success the profile is registered
// in both maps and a validated, defensively cloned copy is returned.
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
// name: the name is lowercased, every non-[a-z0-9] run collapses to a
// single '-' separator, and the result is trimmed — yielding ids that
// satisfy modelProfileIDPattern ("My Tuned Model 2" → "my-tuned-model-2",
// "Феникс" → "" → the "profile" fallback). If the slug is already taken,
// "-2", "-3", … suffixes are tried; past a sanity cap the suffix becomes
// time-based so generation always terminates with a unique, valid id.
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

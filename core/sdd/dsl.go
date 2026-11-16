package sdd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/v0lka/sp4rk/safeio"
)

// Operator definitions — the merge layer over the built-in catalog.
//
// A workspace's framework catalog is assembled from three sources, in this
// order: the embedded built-ins, the operator's global file
// (~/.c0wrk/specs.yaml), and the checkout-local file
// (<workspace>/.c0wrk/specs.yaml). Every source contributes whole framework
// documents; Merge combines them by ID, so a later source either adds a new
// framework or replaces an earlier definition of the same ID outright. Nothing
// can remove a built-in — an operator file only ever adds or overrides.
//
// Loading is fail-soft: the files are optional, a broken YAML stream or an
// invalid document is reported as a warning and skipped while the definitions
// around it still apply, and a startup never fails because of them. The files
// are read, never created — an absent file is the neutral state, not something
// the application materializes.

// Origin identifies the scope a framework definition came from.
type Origin string

const (
	// OriginBuiltin marks a framework compiled into the application.
	OriginBuiltin Origin = "builtin"
	// OriginGlobal marks a framework loaded from the operator's global file.
	OriginGlobal Origin = "global"
	// OriginProject marks a framework loaded from a checkout-local file.
	OriginProject Origin = "project"
)

// Definition is one framework together with its provenance: the scope it was
// loaded from and the document that carried it, so a surface can say where a
// framework came from and an operator can find the file to edit.
type Definition struct {
	Framework
	Origin   Origin // builtin | global | project
	Document string // embedded document name for built-ins, file path otherwise
}

// LoadResult is the merged catalog plus the fail-soft diagnostics produced
// while assembling it. Warnings are operator-facing strings safe to carry in an
// RPC payload; an empty catalog with warnings is a legal, non-fatal outcome.
type LoadResult struct {
	Frameworks []Definition // merged, ordered by Priority then ID
	Warnings   []string     // load diagnostics; nil when every source was clean
}

// ByID returns the merged framework with the given ID.
func (r LoadResult) ByID(id string) (Definition, bool) {
	for _, def := range r.Frameworks {
		if def.ID == id {
			return def, true
		}
	}
	return Definition{}, false
}

// Load assembles the framework catalog from the built-ins and the two operator
// files. An empty path means "this scope has no operator file" and is skipped
// without a warning, as is a path the operator never created. The call never
// returns an error: built-in failures and operator-file faults are reported as
// warnings beside whatever could be loaded.
func Load(globalPath, projectPath string) LoadResult {
	var result LoadResult

	builtins, err := BuiltinDefinitions()
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("sdd: cannot load the built-in frameworks: %v (using the operator files only)", err))
	}
	sets := [][]Definition{builtins}

	for _, source := range []struct {
		origin Origin
		path   string
	}{
		{OriginGlobal, globalPath},
		{OriginProject, projectPath},
	} {
		if strings.TrimSpace(source.path) == "" {
			continue // the caller has no file for this scope
		}
		definitions, warnings := LoadDefinitions(source.origin, source.path)
		result.Warnings = append(result.Warnings, warnings...)
		sets = append(sets, definitions)
	}

	result.Frameworks = Merge(sets...)
	return result
}

// LoadDefinitions reads one operator definitions file, labelling every
// definition with origin and the file path. The load is fail-soft: a missing
// file is the neutral state (no definitions, no warning) and the file is never
// created; an unreadable file or a broken document is a warning while the
// definitions that did parse still apply.
func LoadDefinitions(origin Origin, path string) (definitions []Definition, warnings []string) {
	data, err := safeio.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("sdd: %s: cannot read the operator definitions file: %v (ignoring it)", path, err)}
	}

	frameworks, warnings := ParseDefinitions(path, data)
	definitions = make([]Definition, 0, len(frameworks))
	for _, fw := range frameworks {
		definitions = append(definitions, Definition{Framework: fw, Origin: origin, Document: path})
	}
	return definitions, warnings
}

// ParseDefinitions decodes and validates every framework document in a YAML
// stream — the file shape the operator writes, where each `---`-separated
// document is a complete, independently validated framework (the built-in
// documents are exactly this shape, so an override is a copy edited in place).
//
// Parsing is strict per document (Parse's rules) and fail-soft per stream: an
// invalid document is skipped with a warning naming it and the documents around
// it still apply; a YAML fault that stops the stream keeps the documents
// decoded before it and reports the rest. A document that carries no keys at
// all is blank (an empty file, a trailing `---`) and is skipped silently; a
// document that carries ANY key is decoded and validated like every other
// definition, so a fragment that forgot its `schema`/`id` header is named in the
// warnings instead of vanishing as if it were empty. Within one file a repeated
// ID replaces the earlier definition, with a warning — the merge between sources
// is silent, an in-file duplicate is a typo.
func ParseDefinitions(document string, data []byte) (frameworks []Framework, warnings []string) {
	if strings.TrimSpace(document) == "" {
		document = "document"
	}
	positions := make(map[string]int)
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	// A second, lenient decoder walks the same stream in step with the strict
	// one: it answers the one question the typed decode cannot — whether the
	// document carried any keys. Both decoders advance exactly one document per
	// successful Decode and stop at the same syntax fault, so the indexes stay
	// aligned.
	shadow := yaml.NewDecoder(bytes.NewReader(data))
	for index := 1; ; index++ {
		var node yaml.Node
		nodeErr := shadow.Decode(&node)
		var fw Framework
		err := dec.Decode(&fw)
		if errors.Is(err, io.EOF) {
			return frameworks, warnings
		}
		if err != nil {
			var typeErr *yaml.TypeError
			if !errors.As(err, &typeErr) {
				// A YAML syntax fault: the decoder cannot resume the stream, so
				// the documents already decoded are all that is left.
				warnings = append(warnings, fmt.Sprintf("sdd: %s: cannot parse document %d: %v (the definitions before it are kept)", document, index, err))
				return frameworks, warnings
			}
			// Well-formed YAML this schema cannot decode (an unknown key, a wrong
			// shape) is one skipped document; the decoder resumes at the next one.
			warnings = append(warnings, fmt.Sprintf("sdd: %s: invalid definition: %v (skipped)", documentLabel(document, index), err))
			continue
		}
		// `schema`/`id` are NOT the blankness test: nine more top-level keys
		// exist, and a document carrying any of them without the header is the
		// likeliest operator mistake (a fragment copied out of the guide). Such
		// a document reaches the validator and reports its own faults.
		if nodeErr == nil && blankDocument(&node) {
			continue
		}
		label := documentLabel(document, index)
		if err := fw.normalize(); err != nil {
			warnings = append(warnings, fmt.Sprintf("sdd: %s: %v (definition skipped)", label, err))
			continue
		}
		if err := fw.validate(label); err != nil {
			// A document with several faults reports them together, one line
			// each; callers surface the string verbatim.
			warnings = append(warnings, err.Error())
			continue
		}
		if at, duplicate := positions[fw.ID]; duplicate {
			warnings = append(warnings, fmt.Sprintf("sdd: %s: duplicate framework id %q replaces the earlier definition in this file", label, fw.ID))
			frameworks[at] = fw
			continue
		}
		positions[fw.ID] = len(frameworks)
		frameworks = append(frameworks, fw)
	}
}

// Merge overlays definition sets by ID, earlier sets first: a definition whose
// ID already exists replaces that definition in full (a framework is never
// merged field by field), and an unseen ID is added. Nothing is ever removed,
// so a built-in survives every source. The result is ordered by Priority then
// ID — the same order Builtins uses, which keeps detection independent of the
// order the sources happened to be written in.
func Merge(sets ...[]Definition) []Definition {
	var merged []Definition
	index := make(map[string]int)
	for _, set := range sets {
		for _, def := range set {
			if at, exists := index[def.ID]; exists {
				merged[at] = def
				continue
			}
			index[def.ID] = len(merged)
			merged = append(merged, def)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Priority != merged[j].Priority {
			return merged[i].Priority < merged[j].Priority
		}
		return merged[i].ID < merged[j].ID
	})
	return merged
}

// blankDocument reports whether a decoded YAML document carries no keys at all:
// an empty file, a whitespace-only document, a trailing `---` separator or an
// explicit `null`. Anything with content — including a mapping that carries keys
// the framework schema does not know — is NOT blank, so it reaches the validator
// and is named in the warnings rather than disappearing as if it were empty.
func blankDocument(node *yaml.Node) bool {
	if node == nil {
		return true
	}
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return true
		}
		return blankDocument(node.Content[0])
	}
	switch node.Kind {
	case yaml.MappingNode:
		return len(node.Content) == 0
	case yaml.ScalarNode:
		return node.Tag == "!!null" || strings.TrimSpace(node.Value) == ""
	}
	return false
}

// documentLabel names one document of a stream: the plain label for the first,
// with a positional suffix for the rest, so a warning identifies which document
// of a multi-document file failed.
func documentLabel(document string, index int) string {
	if index <= 1 {
		return document
	}
	return fmt.Sprintf("%s (document %d)", document, index)
}

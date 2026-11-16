package sdd

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// frameworksFS holds the built-in framework documents. They are ordinary DSL
// documents parsed by Parse — there is no privileged in-code representation.
//
//go:embed frameworks/*.yaml
var frameworksFS embed.FS

// BuiltinNames returns the embedded document names (without the directory
// prefix), sorted.
func BuiltinNames() ([]string, error) {
	entries, err := fs.ReadDir(frameworksFS, "frameworks")
	if err != nil {
		return nil, fmt.Errorf("sdd: reading embedded frameworks: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

// Builtins returns every embedded framework, ordered by Priority then ID so the
// first element is the framework a workspace should be attributed to when
// several match.
func Builtins() ([]Framework, error) {
	definitions, err := BuiltinDefinitions()
	if err != nil {
		return nil, err
	}
	frameworks := make([]Framework, 0, len(definitions))
	for _, def := range definitions {
		frameworks = append(frameworks, def.Framework)
	}
	return frameworks, nil
}

// BuiltinDefinitions returns every embedded framework labelled with the
// embedded document it was parsed from and OriginBuiltin, in Builtins order —
// the provenance-carrying form Load merges the operator files onto.
func BuiltinDefinitions() ([]Definition, error) {
	names, err := BuiltinNames()
	if err != nil {
		return nil, err
	}
	definitions := make([]Definition, 0, len(names))
	for _, name := range names {
		document := "frameworks/" + name
		data, err := frameworksFS.ReadFile(document)
		if err != nil {
			return nil, fmt.Errorf("sdd: reading embedded %s: %w", name, err)
		}
		fw, err := Parse(document, data)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, Definition{Framework: fw, Origin: OriginBuiltin, Document: document})
	}
	sort.SliceStable(definitions, func(i, j int) bool {
		if definitions[i].Priority != definitions[j].Priority {
			return definitions[i].Priority < definitions[j].Priority
		}
		return definitions[i].ID < definitions[j].ID
	})
	return definitions, nil
}

// Builtin returns the embedded framework with the given ID.
func Builtin(id string) (Framework, error) {
	frameworks, err := Builtins()
	if err != nil {
		return Framework{}, err
	}
	for _, fw := range frameworks {
		if fw.ID == id {
			return fw, nil
		}
	}
	ids := make([]string, 0, len(frameworks))
	for _, fw := range frameworks {
		ids = append(ids, fw.ID)
	}
	return Framework{}, fmt.Errorf("sdd: unknown built-in framework %q (want one of %s)", id, strings.Join(ids, ", "))
}

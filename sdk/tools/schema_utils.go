package tools

import "encoding/json"

// stripParamsFromSchema removes the named properties from a JSON Schema's
// "properties" object and (if present) from "required". This is used to hide
// parameters that are auto-injected at execution time and should not be visible
// to the LLM. It is shared with ParamManager.
func stripParamsFromSchema(schema json.RawMessage, paramsToRemove map[string]bool) (json.RawMessage, error) {
	if len(paramsToRemove) == 0 {
		return schema, nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(schema, &raw); err != nil {
		return schema, err
	}

	propsRaw, ok := raw["properties"]
	if !ok {
		return schema, nil // no properties, nothing to strip
	}

	var props map[string]json.RawMessage
	if err := json.Unmarshal(propsRaw, &props); err != nil {
		return schema, err
	}

	changed := false
	for p := range paramsToRemove {
		if _, exists := props[p]; exists {
			delete(props, p)
			changed = true
		}
	}
	if !changed {
		return schema, nil
	}

	propsRaw, err := json.Marshal(props)
	if err != nil {
		return schema, err
	}
	raw["properties"] = propsRaw

	// Also clean up the "required" array if present.
	if reqRaw, ok := raw["required"]; ok {
		var req []string
		if err := json.Unmarshal(reqRaw, &req); err == nil {
			filtered := make([]string, 0, len(req))
			for _, r := range req {
				if !paramsToRemove[r] {
					filtered = append(filtered, r)
				}
			}
			if len(filtered) != len(req) {
				filteredRaw, err := json.Marshal(filtered)
				if err != nil {
					return schema, err
				}
				raw["required"] = filteredRaw
			}
		}
	}

	result, err := json.Marshal(raw)
	if err != nil {
		return schema, err
	}
	return result, nil
}

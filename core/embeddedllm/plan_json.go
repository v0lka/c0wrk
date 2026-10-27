package embeddedllm

import "encoding/json"

// UnmarshalJSON decodes into a fresh value so neither absent fields nor
// invalid JSON partially overwrite an earlier plan on a reused receiver;
// unset knobs fall back to the pinned defaults (specs/domains/embedded-llm.md).
func (p *MemoryPlan) UnmarshalJSON(data []byte) error {
	type planJSON MemoryPlan // no methods: avoid recursively calling this decoder
	decoded := planJSON{
		CtxCheckpoints: DefaultCtxCheckpoints,
		CacheIdleSlots: true,
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = MemoryPlan(decoded)
	return nil
}

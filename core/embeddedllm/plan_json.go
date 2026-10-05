package embeddedllm

import "encoding/json"

// UnmarshalJSON preserves the launch policy of manifests written before the
// checkpoint knobs existed. Missing fields inherit the pinned runtime defaults;
// explicit zero and false remain operator choices, not omission sentinels.
// Decode into a fresh value so neither absent fields nor invalid JSON retain or
// partially overwrite an earlier plan on a reused receiver.
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

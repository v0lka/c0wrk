package memory

import (
	"log"
)

// MemorySystem is the aggregate that holds all memory subsystems per AD 5.1
type MemorySystem struct {
	Working    *ContextWindow
	Episodic   *EpisodicMemory
	Semantic   *SemanticMemory
	Procedural *ProceduralMemory
	Reflexion  *ReflexionMemory
}

// MemorySystemConfig holds configuration for creating a MemorySystem.
type MemorySystemConfig struct {
	EpisodicDBPath string
	SemanticDBPath string
	SkillsDir      string
	Embedder       Embedder // can be nil
}

// NewMemorySystem creates and initializes all memory subsystems.
// Individual subsystems that fail to initialize are set to nil with a warning logged.
// Working memory is created externally and set directly (since it's per-request).
func NewMemorySystem(cfg MemorySystemConfig) (*MemorySystem, error) {
	ms := &MemorySystem{}

	// Create EpisodicMemory if EpisodicDBPath is provided
	if cfg.EpisodicDBPath != "" {
		em, err := NewEpisodicMemory(cfg.EpisodicDBPath)
		if err != nil {
			log.Printf("warning: failed to initialize episodic memory: %v", err)
		} else {
			ms.Episodic = em
		}
	}

	// Create SemanticMemory if SemanticDBPath and Embedder are provided
	if cfg.SemanticDBPath != "" && cfg.Embedder != nil {
		sm, err := NewSemanticMemory(cfg.SemanticDBPath, cfg.Embedder)
		if err != nil {
			log.Printf("warning: failed to initialize semantic memory: %v", err)
		} else {
			ms.Semantic = sm
		}
	}

	// Create ProceduralMemory if SkillsDir is provided, runs Scan()
	if cfg.SkillsDir != "" {
		pm := NewProceduralMemory(cfg.SkillsDir)
		if err := pm.Scan(); err != nil {
			log.Printf("warning: failed to scan procedural memory: %v", err)
		}
		ms.Procedural = pm
	}

	return ms, nil
}

// Close closes all closeable memory subsystems.
func (ms *MemorySystem) Close() error {
	var lastErr error

	if ms.Episodic != nil {
		if err := ms.Episodic.Close(); err != nil {
			log.Printf("warning: failed to close episodic memory: %v", err)
			lastErr = err
		}
	}

	if ms.Semantic != nil {
		if err := ms.Semantic.Close(); err != nil {
			log.Printf("warning: failed to close semantic memory: %v", err)
			lastErr = err
		}
	}

	// ProceduralMemory and ContextWindow don't need closing

	return lastErr
}

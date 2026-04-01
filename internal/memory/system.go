package memory

import (
	"context"
	"database/sql"
	"log"

	_ "modernc.org/sqlite" // register SQLite driver
)

// MemorySystem is the aggregate that holds all memory subsystems per AD 5.1
type MemorySystem struct {
	db         *sql.DB // shared connection, unexported
	Working    *ContextWindow
	Episodic   *EpisodicMemory
	Semantic   *SemanticMemory
	Procedural *ProceduralMemory
	Reflexion  *ReflexionMemory
}

// MemorySystemConfig holds configuration for creating a MemorySystem.
type MemorySystemConfig struct {
	DBPath    string   // single DB path for all persistent memory
	SkillsDir string
	Embedder  Embedder // can be nil (semantic memory optional)
}

// NewMemorySystem creates and initializes all memory subsystems.
// Individual subsystems that fail to initialize are set to nil with a warning logged.
// Working memory is created externally and set directly (since it's per-request).
func NewMemorySystem(cfg MemorySystemConfig) (*MemorySystem, error) {
	ms := &MemorySystem{}

	// Open shared database connection if DBPath is provided
	if cfg.DBPath != "" {
		db, err := sql.Open("sqlite", cfg.DBPath)
		if err != nil {
			log.Printf("warning: failed to open database: %v", err)
			return ms, nil
		}

		// Set WAL mode for better concurrency
		if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
			log.Printf("warning: failed to set WAL mode: %v", err)
		}

		ms.db = db

		// Create EpisodicMemory - always create if db is available
		em, err := NewEpisodicMemory(db)
		if err != nil {
			log.Printf("warning: failed to initialize episodic memory: %v", err)
		} else {
			ms.Episodic = em
		}

		// Create SemanticMemory - only if embedder is also provided
		if cfg.Embedder != nil {
			sm, err := NewSemanticMemory(db, cfg.Embedder)
			if err != nil {
				log.Printf("warning: failed to initialize semantic memory: %v", err)
			} else {
				ms.Semantic = sm
			}
		}

		// Create ReflexionMemory - always create if db is available
		rm, err := NewReflexionMemory(db)
		if err != nil {
			log.Printf("warning: failed to initialize reflexion memory: %v", err)
		} else {
			ms.Reflexion = rm
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

// Close closes the shared database connection.
func (ms *MemorySystem) Close() error {
	if ms.db != nil {
		return ms.db.Close()
	}
	// ProceduralMemory and ContextWindow don't need closing
	return nil
}

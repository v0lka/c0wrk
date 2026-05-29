package backend

import (
	"path/filepath"

	"github.com/v0lka/c0wrk/core/skills"
)

// ListSkills returns lightweight descriptors for all available skills,
// including project-local skills from the active project's .agents/skills directory.
func (f *FrontendAPI) ListSkills() []SkillDescriptorDTO {
	if f.app == nil || f.app.Builder() == nil {
		return []SkillDescriptorDTO{}
	}

	baseDirs := f.app.Builder().GetBaseSkillDirs()

	// Prepend project-local skills if an active project is set.
	f.activeProjectMu.RLock()
	wsPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()

	dirs := make([]string, 0, len(baseDirs)+1)
	if wsPath != "" {
		dirs = append(dirs, filepath.Join(wsPath, ".agents", "skills"))
	}
	dirs = append(dirs, baseDirs...)

	if len(dirs) == 0 {
		return []SkillDescriptorDTO{}
	}

	sm := skills.NewSkillManager(dirs, f.logger)
	if err := sm.Scan(); err != nil {
		f.log().Warn("ListSkills scan failed", "error", err, "dirs", dirs)
	}

	descriptors := sm.List()
	result := make([]SkillDescriptorDTO, len(descriptors))
	for i, d := range descriptors {
		result[i] = SkillDescriptorDTO{Name: d.Name, Description: d.Description}
	}
	return result
}

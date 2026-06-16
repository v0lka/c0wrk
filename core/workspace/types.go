package workspace

// FileNode represents a file or directory entry in the workspace tree.
type FileNode struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	IsDir      bool   `json:"is_dir"`
	Icon       string `json:"icon"`
	IconColor  string `json:"icon_color"`
	Hidden     bool   `json:"hidden"`
	GitIgnored bool   `json:"gitignored"`
}

// GitStatusEntry describes the git status of a single file.
type GitStatusEntry struct {
	Status string `json:"status"` // "M", "A", "R", "C", or "U"
	Staged bool   `json:"staged"`
}

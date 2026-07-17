package vectorindex

// NOTE: IsIndexablePath and IsAnyIndexablePath (package-level) used to live in
// indexer.go, where their doc comments claimed they were "intended for the
// workspace watcher". That was misleading: production uses the cached method
// variants (*Indexer.IsAnyIndexablePath / *Manager.IsAnyIndexablePath), which
// avoid re-walking the workspace per call. These package-level forms rebuild a
// full ignore resolver (a workspace walk) on EVERY call, so they are strictly
// test conveniences. They are kept here, in a _test.go file, so a future
// maintainer cannot accidentally wire them onto a hot path.

// IsIndexablePath reports whether absPath (which must be under root) refers to
// a file or directory the indexer would process. It returns false if the path
// or any ancestor directory is hidden (leading dot) or ignored by .gitignore /
// .aiignore.
//
// TEST ONLY: it rebuilds the ignore resolver (walking root) on every call. Do
// not use on a hot path; prefer the cached *Indexer.IsAnyIndexablePath.
func IsIndexablePath(absPath, root string) bool {
	c, _ := buildIgnoreChecker(root)
	return isIndexablePath(absPath, root, c)
}

// IsAnyIndexablePath reports whether at least one of paths is indexable under
// root. It builds the ignore resolver once for the whole batch. An empty/nil
// path list reports false (nothing indexable).
//
// TEST ONLY: it rebuilds the ignore resolver (walking root) on every call. Do
// not use on a hot path; prefer the cached *Indexer.IsAnyIndexablePath.
func IsAnyIndexablePath(paths []string, root string) bool {
	c, _ := buildIgnoreChecker(root)
	for _, p := range paths {
		if isIndexablePath(p, root, c) {
			return true
		}
	}
	return false
}

/** DOM id of a branch section's content region — the collapsible header's
 *  `aria-controls` target. The region renders only while the section is
 *  expanded, so the reference names an element exactly when one exists.
 *  Shared by BranchList (Local/Remote) and BranchDropdown (Worktrees) so
 *  every branch surface mints the same ids. In its own module: component
 *  files must export components only (react-refresh). */
export function branchRegionId(key: string): string {
  return `branch-region-${key}`
}

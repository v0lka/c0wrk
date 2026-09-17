// Paper workspace section catalog.
//
// Kept in its own module (not alongside the PaperWorkspace component) so the
// component file exports components only — required for React Fast Refresh.

export type PaperSection =
  | 'overview'
  | 'note'
  | 'appraisal'
  | 'compare'
  | 'flashcards'
  | 'source'
  | 'literature'

/** The workspace sections, in display order. */
export const PAPER_WORKSPACE_SECTIONS: ReadonlyArray<{ id: PaperSection; label: string }> = [
  { id: 'overview', label: 'Overview' },
  { id: 'note', label: 'Note' },
  { id: 'appraisal', label: 'Appraisal' },
  { id: 'compare', label: 'Compare' },
  { id: 'flashcards', label: 'Flashcards' },
  { id: 'source', label: 'Source' },
  { id: 'literature', label: 'Literature' },
]

// parsePlusTokens extracts "+token" substrings from a query string.
// Used by the vector search UI to let users pin must-match tokens inline.
// Returns the stripped query and the list of tokens (without leading '+').
// Matches contiguous non-whitespace runs after '+'. Empty strings yield [].
export function parsePlusTokens(raw: string): { query: string; tokens: string[] } {
  const tokens: string[] = []
  const stripped = raw
    .replace(/(^|\s)\+(\S+)/g, (_, prefix: string, tok: string) => {
      if (tok) tokens.push(tok)
      return prefix
    })
    .replace(/\s+/g, ' ')
    .trim()
  return { query: stripped, tokens }
}

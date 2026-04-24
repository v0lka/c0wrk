---
description: Performs specification compliance check
---

# Specification Compliance Check

## Overview
This command performs a comprehensive compliance check between the codebase and specification documents, identifying discrepancies in both directions:
- **Code vs Specification**: Features implemented in code but not documented in specs
- **Specification vs Code**: Features documented in specs but not implemented in code

## Workflow

### Phase 1: Analysis
1. Parse all specification documents in `.qoder/specs/` directory
2. Analyze the codebase using Bifrost LSP tools to understand implementation
3. Cross-reference specifications with actual code structure
4. Generate a comprehensive discrepancy report
5. Wait for user decisions (Phase 2)

### Phase 2: User Decision
For each identified discrepancy, present:
- **Discrepancy Type**: Code-only, Spec-only, or Mismatch
- **Location**: File path and line numbers
- **Description**: Clear explanation of the discrepancy
- **Options**:
  - `[C]` Code is correct - update specification
  - `[S]` Specification is correct - update code
  - `[B]` Both need alignment - specify desired state
  - `[I]` Ignore - this discrepancy is intentional

### Phase 3: Resolution Planning
1. Aggregate all user decisions
2. Build a prioritized resolution plan with:
   - Specification updates needed
   - Code changes required
   - Estimated impact and effort
   - Dependencies between changes
3. Present plan for user confirmation

### Phase 4: Implementation
Upon user confirmation:
1. Execute specification updates (modify `.md` files in `.qoder/specs/`)
2. Execute code changes using appropriate tools
3. Run `/issues-check` to verify all changes (per Completeness Rule)
4. Generate compliance report showing resolved items

## Analysis Methods

### Code Analysis
Uses Bifrost LSP tools to extract:
- `mcp__get_document_symbols` - Understand code structure
- `mcp__find_usages` - Verify implementation completeness
- `mcp__get_type_definition` - Validate type specifications
- `mcp__get_hover_info` - Check function signatures

### Specification Analysis
Parses specification documents to extract:
- Required features and components
- API contracts and interfaces
- Type definitions and schemas
- Behavioral requirements
- Non-functional requirements

### Cross-Reference Logic
Matches specifications to code by:
- Component names and identifiers
- Function signatures and types
- API endpoints and routes
- Data structures and models

## Output Format

### Discrepancy Report
=== SPECIFICATION COMPLIANCE REPORT ===

📋 SPECIFICATIONS ANALYZED: X files
📦 CODE MODULES ANALYZED: Y files
⚠️  TOTAL DISCREPANCIES: Z

--- CODE-ONLY FEATURES (Not in Specs) ---
[1] Feature: /path/to/file.go:123
    Description: Function `HandleRequest` implements authentication
    Decision: [C]ode is truth | [S]pec is truth | [I]gnore

[2] Feature: /path/to/component.go:456
    Description: Type `UserProfile` has additional field `avatar`
    Decision: [C]ode is truth | [S]pec is truth | [I]gnore

--- SPEC-ONLY FEATURES (Not in Code) ---
[3] Feature: specs/api.md:78
    Description: Endpoint `POST /api/logout` is specified but not implemented
    Decision: [C]ode is truth | [S]pec is truth | [I]gnore

[4] Feature: specs/types.md:34
    Description: Type `Settings.theme` is specified but missing in code
    Decision: [C]ode is truth | [S]pec is truth | [I]gnore

--- MISMATCHES (Different Implementations) ---
[5] Mismatch: /path/to/handler.go:89 vs specs/api.md:45
    Spec: Returns `User` object
    Code: Returns `UserDTO` object
    Decision: [C]ode is truth | [S]pec is truth | [B]oth need change | [I]gnore

=== END REPORT ===

### Resolution Plan
=== RESOLUTION PLAN ===

📝 SPECIFICATION UPDATES: X items
- Update specs/api.md:123 - Add `HandleRequest` authentication behavior
- Update specs/types.md:456 - Add `UserProfile.avatar` field

🔧 CODE CHANGES: Y items
- Implement POST /api/logout in handlers/auth.go
- Add `theme` field to Settings struct in types/settings.go
- Refactor handler.go:89 to return User instead of UserDTO

✅ VERIFICATION STEPS:
- Run go build ./...
- Run golangci-lint run
- Run go test ./... -v -race
- Verify coverage ≥80%

PROCEED WITH IMPLEMENTATION? [Y/n]

## Integration with Other Rules

- **Completeness Rule**: Every change must pass `/issues-check` verification
- **Bifrost Rule**: Use LSP tools for impact analysis before modifications
- **Reinvention Rule**: Verify changes don't duplicate existing functionality
- **Context7 Rule**: Use external docs for library-specific implementations

## Error Handling

- If specifications are ambiguous, request clarification from user
- If code structure is too complex to analyze, report partial results
- If user decisions conflict, highlight conflicts and request resolution
- If implementation fails verification, report issues and revert if necessary

## Notes

- All specification files in `.qoder/specs/` are considered authoritative sources
- Code in `vendor/` or `third_party/` directories is excluded from analysis
- Test files are analyzed separately and marked as such in reports
- Generated code (if marked) is noted but not treated as discrepancies

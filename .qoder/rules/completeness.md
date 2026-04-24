---
trigger: model_decision
description: When changes are made to the code base
---

# Completeness Rule

**Trigger:** `always_on`

## Overview
This rule ensures that all tasks and quests are completed with comprehensive quality checks before being marked as done.

## Core Requirement
When finishing any task or quest, you **MUST** execute the `/issues-check` command to verify project health.

## Mandatory Checks

### 1. Build Verification
go build ./...
npx tsc --noEmit
**Must achieve:**
- Zero compilation errors or warnings 
- All dependencies resolved
- No type mismatches

**Fix immediately:**
- Import errors
- Type incompatibilities
- Syntax errors
- Deprecated API usage

### 2. Linter Compliance
golangci-lint run
**Must achieve:**
- Zero linter warnings
- Zero linter errors
- Full compliance with Go best practices

**Fix immediately:**
- Code quality issues
- Style violations
- Potential bugs
- Security concerns

### 3. Test Success
go test ./... -v -race -coverprofile=coverage.out
**Must achieve:**
- 100% test pass rate
- No race conditions
- Test coverage ≥80%

**Fix immediately:**
- Failing tests
- Race conditions
- Coverage gaps
- Broken test assertions

## Execution Workflow

1. **Complete the primary task** - implement the requested feature or fix
2. **Run `/issues-check`** - execute comprehensive verification
3. **Address all issues** - fix any problems discovered
4. **Re-verify if needed** - run checks again until all pass
5. **Confirm completion** - only then mark task as done

## Success Criteria (All Required)

- ✅ Zero build errors
- ✅ Zero linter warnings
- ✅ All tests passing (100%)
- ✅ No race conditions detected
- ✅ Test coverage ≥80%
- ✅ No introduced TODO/FIXME for core functionality

## Important Notes

- **Never skip checks** - completeness is non-negotiable
- **Fix, don't suppress** - address root causes, not symptoms
- **Document decisions** - explain any trade-offs made
- **Maintain compatibility** - preserve existing behavior unless explicitly changing it
- **Verify before claiming done** - completion means verified completion

## Example Workflow

1. Implement feature X
2. Run: /issues-check
3. Observe: 2 linter warnings, 1 failing test
4. Fix: Address warnings and test failure
5. Run: /issues-check
6. Result: All checks pass ✅
7. Confirm: Task complete

## Non-Compliance

Tasks are **NOT complete** if:
- Build has errors
- Linter reports warnings
- Tests are failing
- Race conditions exist
- Coverage is below threshold
- `/issues-check` was not executed

## Integration with Other Rules

This rule works in conjunction with:
- **Bifrost Rule**: Use LSP tools for impact analysis before changes
- **Reinvention Rule**: Verify no unnecessary code was introduced
- **Context7 Rule**: Ensure external dependencies are correctly used

## Remember

**Complete ≠ Implemented**  
**Complete = Implemented + Verified + Passing All Checks**

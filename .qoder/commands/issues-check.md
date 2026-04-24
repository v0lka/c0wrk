---
description: Check for issues and fix them all
---

# Check and Fix All Issues

This command performs a comprehensive check and fix of all project issues including build errors, linter warnings, and test failures.

## Steps

### 1. Build the Project
go build ./...
- Compiles all packages in the project
- Identifies compilation errors
- Verifies all dependencies are available

**Fix approach:**
- Resolve import errors
- Fix type mismatches
- Correct syntax errors
- Update deprecated API usage

### 2. Run Linter
golangci-lint run
- Performs static analysis
- Checks code quality and style
- Identifies potential bugs and inefficiencies

**Fix approach:**
- Fix reported warnings and errors
- Apply recommended code improvements
- Ensure compliance with Go best practices
- Address security concerns

### 3. Run All Tests
go test ./... -v -race -coverprofile=coverage.out
- Executes all unit and integration tests
- Checks for race conditions with `-race` flag
- Generates coverage report

❗ Please NOTE: tests in Go doesn't provides a summary at the end of the output, so you must examine the entire output for failed tests.

**Fix approach:**
- Debug failing tests
- Fix race conditions
- Update tests for API changes
- Ensure test coverage ≥80%

## Success Criteria

All of the following must be achieved:
- ✅ Zero build errors
- ✅ Zero linter warnings
- ✅ All tests passing (100% pass rate)
- ✅ No race conditions detected
- ✅ Test coverage ≥80%

## Execution Order

1. **Build first** - catch compilation errors early
2. **Lint second** - fix code quality issues
3. **Test last** - verify functionality after fixes

## Notes

- Each step must complete successfully before proceeding to the next
- All issues must be fixed, not suppressed or ignored
- No TODO/FIXME comments should be introduced for core fixes
- Changes should maintain backward compatibility when possible

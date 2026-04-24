---
trigger: model_decision
description: When the tests modification is needed
---

# Test Modification Rule

## Purpose
Protect existing test integrity while allowing necessary updates with user oversight.

## Core Principle
**Tests are the contract of your codebase** - they define expected behavior and catch regressions. Modifying tests should be a deliberate, justified decision.

## Rule: No Unsolicited Test Edits

### Before Modifying Any Existing Test:

1. **Stop and Analyze**
   - Why does this test need to change?
   - Is it because:
     - The test is wrong/flaky? (rare)
     - The behavior is intentionally changing? (needs approval)
     - The test is blocking new functionality? (needs discussion)

2. **Present to User**
   You MUST explain:
   I need to modify the test: `TestFunctionName`
   
   Current test behavior:
   - [What it currently tests]
   
   Reason for change:
   - [Why modification is needed]
   
   Proposed change:
   - [What will be modified]
   
   Impact:
   - [What behavior will no longer be caught]
   - [What new behavior will be tested]
   
   Do you approve this test modification?

3. **Wait for Approval**
   - Do NOT proceed without explicit user confirmation
   - If user rejects, explore alternative solutions that preserve tests

### Acceptable Test Changes (Still Require Justification)

1. **Fixing Broken Tests After Code Changes**
   - When implementation correctly changes behavior
   - When test expectations need updating to match new contract
   - Example: "Updated expected value from 5 to 10 because the algorithm now includes X"

2. **Fixing Flaky Tests**
   - Adding proper waits/synchronization
   - Fixing race conditions in test code
   - Improving test isolation

3. **Improving Test Quality**
   - Adding better assertions
   - Improving test clarity
   - Adding missing test cases (additions are better than modifications)

### When Adding New Tests (Preferred)

Adding tests is always better than modifying them:
- **Do add** comprehensive test coverage for new features
- **Do add** edge case tests
- **Do add** regression tests for bugs
- **Do add** tests before modifying existing ones

### Red Flags (Almost Always Require Discussion)

🚫 **STOP and consult user if:**
- Commenting out existing test cases
- Removing assertions
- Making test expectations more lenient
- Changing test from failure to passing by weakening assertions
- Modifying tests "to make CI pass"

### Example Good Communication

## Test Modification Request

**Test File:** `pkg/parser/parser_test.go`
**Test Name:** `TestParseComplexExpression`

**Current Behavior:**
Test expects `ParseExpression("a AND b")` to return AST with 3 nodes

**Why Change Needed:**
New optimization combines adjacent AND operations into single node

**Proposed Change:**
Update expectation from 3 nodes to 1 node

**Verification:**
- New behavior is correct: ✅ (optimization is valid)
- Old behavior was correct: ✅ (unoptimized was valid too)
- Benefit: 40% faster parsing

**Impact:**
- Test will no longer catch if optimization regresses
- Mitigation: Adding dedicated optimization test

**Request:** May I proceed with this test modification?

### Integration with Completeness Rule

Even when modifying tests with approval:
- Must run `/issues-check` after changes
- All tests must still pass
- Coverage must not decrease
- No new race conditions

### Exception: Test-Driven Development

When actively practicing TDD with user:
1. User writes failing test first
2. You implement code
3. Test modification is implicit in the workflow

But still document what changed and why.

## Summary

**Default Action:** PRESERVE existing tests  
**Required Action:** JUSTIFY and GET APPROVAL before modification  
**Preferred Action:** ADD new tests instead of modifying existing ones  
**Forbidden Action:** Silently weakening test assertions

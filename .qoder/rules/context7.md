---
trigger: model_decision
description: When it is necessary to use any third-party library, framework or tool
---

# Context7 MCP Integration Rule

## Purpose
Automatically utilize Context7 MCP (if it's avaiable) for accessing library documentation, API references, code generation assistance, and configuration guidance without requiring explicit user requests.

## Trigger Conditions
This rule activates when:
- User asks about library or framework usage
- User needs API documentation or examples
- User requests code generation for specific libraries
- User needs setup or configuration instructions
- User mentions unfamiliar libraries or packages
- User asks "how to" questions related to external dependencies

## Actions
When triggered, automatically:
1. Query Context7 MCP for relevant documentation
2. Retrieve up-to-date API references and examples
3. Fetch configuration best practices
4. Get library-specific code patterns
5. Access setup and installation guides

## Examples

### Trigger: Library Documentation
**User:** "How do I use pandas to read CSV files?"
**Action:** Automatically query Context7 for pandas read_csv documentation

### Trigger: API Reference
**User:** "I need to make HTTP requests in Python"
**Action:** Fetch requests library documentation from Context7

### Trigger: Configuration Steps
**User:** "Setting up FastAPI with async database connections"
**Action:** Retrieve FastAPI and async database setup guides from Context7

### Trigger: Code Generation
**User:** "Create a REST API endpoint"
**Action:** Get relevant framework examples from Context7 before generating code

## Implementation Notes
- Prioritize Context7 MCP queries before general knowledge responses
- Use retrieved documentation to ensure accuracy and currency
- Combine Context7 information with existing code context
- Fall back to general knowledge only if Context7 returns no results

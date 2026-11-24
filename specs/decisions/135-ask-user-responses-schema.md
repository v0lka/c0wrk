# ADR-135: Keep option-level recommended out of the ask_user schema

## Status

Accepted

## Context

[Issue #263](https://github.com/v0lka/c0wrk/issues/263) reports that the ChatGPT Responses API rejects the entire request when the `ask_user` schema contains an untyped option-level `recommended` property. The Responses adapter in sp4rk uses strict function calling and makes every declared property required. ADR-133 declared the option-level key solely to give misplaced arguments a tailored validation error.

## Decision

The option schema declares `label`, `value`, and the optional display-only `description`. A question's `recommended` field takes an array of option values. The tool description gives models the correct placement.

The pre-dispatch validator rejects an option-level `recommended` as an unknown property. Direct calls to `AskUserTool.Execute` receive the placement-specific validation error.

## Consequences

The strict Responses schema contains only supported option arguments. Pre-dispatch errors include the option path and key, and the tool description provides the correction.

## Alternatives Considered

- Give the option-level key a concrete value type. Strict Responses would require the model to supply a value that the tool rejects.
- Declare a null-only placeholder. Strict Responses would require a placeholder in every option, adding arguments and normalization without carrying recommendation data.

## Related

- [ADR-133](133-ask-user-option-descriptions.md) defines option descriptions.
- [Built-in tools](../domains/tool-system/builtins.md#ask_user-c0wrk-specific) describes the current contract.

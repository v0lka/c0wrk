A plan execution completed but some acceptance criteria were not met. Revise the plan to address the failures.

Original plan:
ORIGINAL-PLAN

Completed steps with results:
COMPLETED-STEPS

Failed step: FAILED-STEP

CURRENT-REFLECTION

PREVIOUS-SESSION-REFLECTIONS

Acceptance criteria:
ACCEPTANCE-CRITERIA

WORKSPACE-PATH

Create an updated plan following these rules:

1. PRESERVE successful steps: reuse their exact step IDs (e.g., "step_1", "step_2") unchanged in the new plan. Their outputs will be reused automatically.
2. Only ADD or REPLACE steps that directly address the failed criteria.
3. New steps MUST use IDs that don't conflict with existing step IDs (continue numbering from the highest existing ID).
4. Keep the plan as close to the original as possible — minimal targeted changes, not a complete rewrite.
5. If two sequential steps failed for related reasons, consider merging them into a single step.
6. If previous session reflections show a repeating failure pattern with the same root cause across 2+ attempts, consider a broader structural change.

Respond with a JSON Plan object:
{"steps": [{"id": "step_1", "description": "...", "depends_on": [], "parallelizable": true, "estimated_tools": ["tool1"], "relevant_ac": ["ac_1"]}]}

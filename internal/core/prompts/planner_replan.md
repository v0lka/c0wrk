A plan execution partially completed but a step failed. Revise the remaining plan.

Original plan:
ORIGINAL-PLAN

Completed steps with results:
COMPLETED-STEPS

Failed step: FAILED-STEP

REFLECTION

Acceptance criteria:
ACCEPTANCE-CRITERIA

Create an updated plan. Do NOT redo completed steps unless the reflection indicates they were wrong.
Respond with a JSON Plan object:
{"steps": [{"id": "step_1", "description": "...", "depends_on": [], "parallelizable": true, "estimated_tools": ["tool1"], "relevant_ac": ["ac_1"]}]}

A plan execution partially completed but a step failed. Revise the remaining plan.
All step descriptions must be in English regardless of the original task language.

Original plan:
ORIGINAL-PLAN

Completed steps with results:
COMPLETED-STEPS

Failed step: FAILED-STEP

REFLECTION

PREVIOUS-REFLECTIONS

Acceptance criteria:
ACCEPTANCE-CRITERIA

WORKSPACE-PATH
Create an updated plan. Do NOT redo completed steps unless the reflection indicates they were wrong.
If previous session reflections show a repeating failure pattern, design a structurally different approach rather than minor variations of the failed plan.
Respond with a JSON Plan object:
{"steps": [{"id": "step_1", "description": "...", "depends_on": [], "parallelizable": true, "estimated_tools": ["tool1"], "relevant_ac": ["ac_1"]}]}

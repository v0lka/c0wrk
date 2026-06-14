## Guidance — Balance

Apply BRANCH/EVALUATE when decomposing:

- BRANCH multiple decompositions; EVALUATE which keeps each step bounded and context-safe
- Decompose research-heavy tasks into multiple bounded-area steps rather than one monolithic "read everything" step
- Let the coder verify as they go rather than creating separate verify steps
- Ensure each step produces concrete progress toward the goal
- Merge related requirements only when a single executor can complete them without context overflow
- If EVALUATE reveals an implicit dependency between supposedly parallel steps, BACKTRACK and restructure

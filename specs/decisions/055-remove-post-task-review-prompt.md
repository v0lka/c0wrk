# ADR-055: Remove the Post-Task Review Prompt

## Status

Accepted — removes the post-task activation path of the review feature ([review.md](../domains/review.md)) and, pre-release, the `review_prompt` sub-policy that [ADR-053](./053-silent-mode.md) had defined for it (D3 now covers three sub-policies; no released build ever shipped a silent-mode `review_prompt` knob).

## Context

The code-review feature had two activation paths: the manual **Review** button in the changes toolbar, and an automatic post-task prompt — a `review_prompt` card injected into the chat on every successful `task_complete` when the working tree had uncommitted changes and the prompt had not yet been shown for that task.

In practice the automatic path fired on **nearly every task**. A real working tree is perpetually dirty: untracked scratch files, notes, WIP from earlier tasks, and half-staged follow-ups mean "git status is non-empty" is the norm, not a signal that a finished task left something worth reviewing. A prompt that appears after every task is noise — the user learns to click "Decline" without reading it, which is precisely the approval-fatigue failure mode (ASI09) c0wrk's own security documentation warns confirmation prompts must avoid. The card consumed attention without carrying information: the changes toolbar already shows the changed-file set at all times, and the review loop already reopens the review page after each task while it is active.

The noise also bred complexity: silent mode needed a fourth sub-policy (`review_prompt`: `suppress`/`allow`) solely to keep the unattended posture from emitting the card — a config knob, a settings editor row, a store flag (`promptShownForTask`), an RPC (`SaveReviewPrompt`), and a specialized scroll-reveal special case, all in service of a prompt nobody wanted every time.

## Decision

1. **Remove the post-task review prompt entirely.** The injection path (frontend `task_complete` handling), the `ReviewPromptBlock` card, the `review_prompt` DisplayItem kind (chat DisplayItem kinds drop from 21 to 20), the `SaveReviewPrompt` RPC with its `ReviewPromptMessage` type, and the `promptShownForTask` zustand persistence are deleted.
2. **Activation is the Review button only.** The manual entry point (changes toolbar → review page) is unchanged, as is the whole review loop: submitting comments sends them with `reviewMode=true`, and while the loop is active each successful `task_complete` in a CODE project with uncommitted changes auto-reopens the review page with a fresh diff.
3. **Silent mode performs no post-task UI interception at all.** The loop auto-reopen is suppressed unconditionally in silent mode — no sub-policy, no knob. `security.silent_mode` drops to **three** sub-policies (`tool_confirm`, `step_limit`, `ask_user`).
4. **Legacy data degrades gracefully.** Persisted chat rows with the legacy `review_prompt` role keep their stable ids (`review-prompt-{prompt_id}`) and render as muted status service lines via the legacy role map (the same compat pattern as `autonomy_decision`); no migration is written. A config still carrying `security.silent_mode.review_prompt` loads with the stale key ignored (non-strict unmarshal, pinned by `TestSilentMode_RemovedReviewPromptKeyIgnored`).

## Consequences

**Positive**

- Per-task confirmation noise disappears; the user's attention is reserved for prompts that gate something (tool confirmations, step limits, questions).
- The silent-mode surface shrinks by one sub-policy across config schema, validation, settings UI, and docs.
- One fewer frontend↔backend RPC, one fewer persisted store key, one fewer DisplayItem kind, and one fewer special case in the chat scroll manager.

**Negative / accepted costs**

- A user who relied on the card as a "go review now" reminder must enter review manually; the changes toolbar's changed-files indicator and the still-active review loop cover the discovery need.
- Historical `review_prompt` rows render as plain status lines instead of interactive cards — acceptable: their decisions were already recorded when made, and the card's actions no longer exist.

**Security impact:** none. The removal deletes a UI affordance, not a gate; every security policy is untouched.

## Alternatives Considered

- **Keep the card but fire it only above a change-size threshold.** Rejected: any threshold is a guess, and dirtiness is not correlated with review-worthiness — a one-file change may need review while a hundred-file regeneration does not.
- **Demote the card to a passive (non-blocking) notice.** Rejected: it would duplicate the changes toolbar's existing changed-files indicator while keeping the injection path and its special cases alive.
- **Emit a toast/sound instead.** Rejected: same every-task noise in another channel, plus a new channel to maintain.
- **Remove only the card, keep the `review_prompt` silent-mode sub-policy.** Rejected: a sub-policy for a prompt that no longer exists is dead config surface that must still validate, document, and render.

## References

- [specs/domains/review.md](../domains/review.md) — the review feature (manual entry + loop, post-task prompt removed)
- [ADR-053](./053-silent-mode.md) — the autonomy axis and the silent-mode sub-policies (D3 amended to three)
- [specs/domains/frontend/rendering.md](../domains/frontend/rendering.md) — DisplayItem kinds (20) and the legacy `review_prompt` role mapping
- [specs/architecture/security-model.md](../architecture/security-model.md) — silent-mode sub-policy list

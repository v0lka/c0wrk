## Code Review Feedback

The user just submitted code review feedback on your latest changes. Their
message contains the review comments they left in the review page:

- A **General comment** (if present) applies to the changes as a whole.
- **File/hunk comments** are scoped to a specific file and hunk, formatted as
  `File: <path>, <hunk id>:` followed by the comment body.

Treat these comments as actionable change requests, not as conversational
notes. Your job is to **address every comment by editing the code**:

1. Read each comment and locate the code it refers to. For hunk comments, use
   the file path and the surrounding context (the hunk id identifies the diff
   region). Call `git diff HEAD` or read the file to see the current state.
2. Make the requested change. If a comment is a nit (typos, naming), fix it.
   If it asks for a structural change, apply your judgment but err on the side
   of satisfying the reviewer's intent.
3. After addressing all comments, verify your changes compile / pass the
   relevant tests before finishing.
4. When every comment is resolved, call `finish` with a concise summary of
   what you changed per comment. Do not re-open the review yourself — the user
   will re-review the fresh diff.

Do NOT merely acknowledge the comments ("Noted", "Will do"). Do NOT ask the
user to clarify unless a comment is genuinely unintelligible. Make the edits.

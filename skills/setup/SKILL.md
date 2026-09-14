
# Setup

Write `~/.grok/rules/vvvdwbvvv-models.md`, an always-applied rule that sets this plugin's model per role. The skills read it and fall back to their inline defaults when a line is absent, so this is an override layer, not a requirement.

## Steps

### 1. Detect available models

Enumerate the model slugs you can pass to a `spawn_subagent` in this session; that is the dependable source. Run `grok models` if the CLI is available. If you cannot detect any, ask the user to paste the slugs they have access to. Never write a real slug you have not confirmed is available. The aliases `inherit-parent` and `auto` are always valid even though they are not detected slugs.

### 2. Load current state

The default role-to-model mapping is the rule shape shown in step 5 below. If `~/.grok/rules/vvvdwbvvv-models.md` already exists, read it and treat its values as the current choices. Otherwise start from those defaults.

### 3. Map and confirm

Show every role with its current model, marking any real slug not in the detected set as needing a choice. Ask whether to accept as-is or change specific roles, offering the detected models plus `inherit-parent` and `auto` (both mean: this role runs on the parent chat model, which is how Auto users stay on Auto) as the options. Prefer AskQuestion over free text. For panel roles (how critics, arena runners, architect runners, interrogate reviewers) the value is a list, and one subagent runs per entry, alias entries included, so the list length sets the count. `arena cross-judge pool` is also a list, but Arena selects one value from it whose model family differs from the parent's when possible. `swarm workers` is the default model for every worker unless a race or comparison assigns another model per arm.

### 4. Validate

Every real slug written must be in the detected set; `inherit-parent` and `auto` always pass. If a chosen real slug is not available, stop and ask again. A rule pointing at a model the user cannot use breaks every delegation that reads it.

### 5. Write the rule

Write `~/.grok/rules/vvvdwbvvv-models.md` with one line per role, using the same labels vvvdwbvvv uses. Overwrite the whole file so re-runs stay idempotent. Shape:

```
# vvvdwbvvv model configuration. One line per role. Delete a line to fall back to the skill default.
# `inherit-parent` or `auto` as a value: the role runs on the parent chat model (omit spawn_subagent `model`). Alias entries in a panel list still count toward its fan-out.
feature, refactoring: grok-4.5
bug-fix: grok-4.6
perf-issue: grok-4.6
hillclimb: grok-4.6
judgment and prose: grok-4.6
hardest tasks: grok-4.6
how explorer: grok-4.5
how explainer: grok-4.6
how critics: grok-4.6, grok-4.5
why investigators: grok-4.5
why synthesizer: grok-4.6
reflect tooling: grok-4.6
reflect judgment, divergent, synthesizer: grok-4.6
arena runners: grok-4.6, grok-4.5
arena cross-judge pool: grok-4.6, grok-4.5
swarm workers: grok-4.5
architect runners: grok-4.6, grok-4.5
interrogate reviewers: grok-4.6, grok-4.5
```

### 6. Confirm

Tell the user the rule was written and that it applies to new sessions. Re-running this skill updates it.

### 7. Offer a verification skill (optional)

Check whether the project has a way to drive the real app for proof (a `verify-*` skill, or an existing harness). If not, offer once: "want a project-local verification skill, so agents can drive the app the way a user does and prove changes work? I can generate one with /create-verification-skill." On yes, invoke `/create-verification-skill` (resolves wherever this plugin is installed — workspace, user, or plugin). On no, move on without pushing.

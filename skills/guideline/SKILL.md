---
name: guideline
description: >
  Shared engineering principles for launch.
  Not a task entry point. launch reads this index, then opens matching leaves.
disable-model-invocation: true
---

# guideline

Read this file at task start. Open a leaf only when its trigger matches.
Cite a principle in the reply only if you read that leaf this session.
Name the decision it changed. A citation with no decision is name-dropping.

Short names are the API. Do not invent aliases.

## Contract

- `launch` consumes this skill. This skill does not pick playbooks.
- Apply matching leaves only. Do not apply the whole set.
- Resolve `leaves/<slug>.md` relative to this directory.

## Core

| slug | trigger | leaf |
|---|---|---|
| laziness-protocol | refactor, fat diff, new layer or signal threading | leaves/laziness-protocol.md |
| foundational-thinking | before writing logic | leaves/foundational-thinking.md |
| redesign-from-first-principles | new requirement bolted onto an old design | leaves/redesign-from-first-principles.md |
| attack-the-premise | two or more fixes sharing one premise failed the same gate | leaves/attack-the-premise.md |
| subtract-before-you-add | sequencing an add, refactor, or rewrite | leaves/subtract-before-you-add.md |
| minimize-reader-load | code that is hard to trace | leaves/minimize-reader-load.md |
| outcome-oriented-execution | phased rewrite or migration | leaves/outcome-oriented-execution.md |
| experience-first | product or UX tradeoff | leaves/experience-first.md |
| exhaust-the-design-space | novel interaction or architecture, no precedent | leaves/exhaust-the-design-space.md |
| build-the-lever | non-trivial work that should be rerun by a reviewer | leaves/build-the-lever.md |

## Architecture

| slug | trigger | leaf |
|---|---|---|
| model-the-domain | stateful logic, heavy branching, repeated shape | leaves/model-the-domain.md |
| boundary-discipline | validation, errors, framework adapters | leaves/boundary-discipline.md |
| type-system-discipline | designing types or signatures | leaves/type-system-discipline.md |
| make-operations-idempotent | commands that retry or crash | leaves/make-operations-idempotent.md |
| migrate-callers-then-delete-legacy-apis | new internal API with old callers | leaves/migrate-callers-then-delete-legacy-apis.md |
| separate-before-serializing-shared-state | concurrent actors writing the same file, branch, or key | leaves/separate-before-serializing-shared-state.md |

## Verification

| slug | trigger | leaf |
|---|---|---|
| prove-it-works | before declaring done | leaves/prove-it-works.md |
| fix-root-causes | debugging | leaves/fix-root-causes.md |
| sequence-verifiable-units | multi-step work, stacked commits or PRs | leaves/sequence-verifiable-units.md |
| test-behavior-not-implementation | writing or keeping a test | leaves/test-behavior-not-implementation.md |

## Delegation

| slug | trigger | leaf |
|---|---|---|
| guard-the-context-window | context filling up | leaves/guard-the-context-window.md |
| never-block-on-the-human | tempted to ask on reversible work | leaves/never-block-on-the-human.md |

## Meta

| slug | trigger | leaf |
|---|---|---|
| encode-lessons-in-structure | same instruction written a second time | leaves/encode-lessons-in-structure.md |

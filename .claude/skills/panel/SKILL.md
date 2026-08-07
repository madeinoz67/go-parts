---
name: panel
description: >-
  Run competing positions against each other and come out with a DECISION, not a pile of
  opinions. Use when a genuine fork has to be resolved and the evidence does not obviously
  pick a side: a doctrine question (what go-parts should be), a design that returned two
  defensible shapes, or a measurement that contradicts something already landed. Independent
  panelists, a decision rule written before anyone runs, and a judge whose powers are
  deliberately bounded.
---

# panel — dialectical inquiry that terminates

Also called adversarial collaboration, red-teaming, or a judge panel. The value is not
"more opinions." It is **independence followed by reconciliation against a rule written in
advance**, which is what stops the exercise from becoming a summary nobody can act on.

## When it is allowed to run

Any ONE of these qualifies. Nothing else does:

1. A **Tier-3 or doctrine fork** — a decision on a hard-to-reverse surface (concurrency §5.14,
   schema/migration §5.13, secrets §5.18, auth seams §5.8), one that changes what go-parts
   means, or one that contradicts a PRD principle.
2. **A design returned two or more defensible shapes** (in a `docs/superpowers/` design) and
   could not choose between them on the evidence available.
3. A **measurement contradicts a landed invariant or a recorded decision.**

Anything else: pick the defensible default and proceed. A panel is a real cost — three
deep-reasoning runs plus a judge — and the gate has to be worth it. **Do not run a panel on
trivia, and do not run one to avoid making a call you could make.**

## The procedure

### 1. Write the question and the decision rule FIRST

Before any panelist runs, write to the design doc (`docs/superpowers/`) — or a scratch file
if no design exists yet:

- the exact question, phrased so it can be answered;
- what result picks A, what picks B, what would pick something else;
- what result means **the panel was the wrong instrument** and the question needs measurement
  or an owner decision instead.

A rule written after the arguments arrive is not a rule, it is a rationalization.

### 2. Three independent panelists, with different MANDATES

Not different personalities. Different jobs:

- **Argue A** on the strongest available evidence.
- **Argue B** on the strongest available evidence.
- **Reject both and find a third shape.**

That third mandate is not a courtesy — it is the one that has repeatedly produced the answer.
When both options on the table are wrong, only a panelist charged with rejecting both tends to
find the third shape nobody proposed.

Run them **concurrently and blind** — no panelist sees another's output. Use different models
where you can; the independence is the point. Each panelist reads the real code, the PRD, and
the relevant design doc, and cites `file:line`. A panelist that reasons from the brief alone is
producing an opinion, and opinions are what this procedure exists to avoid.

### 3. The judge, with bounded powers

The judge may do exactly three things:

1. **Mark each claim** verified, refuted, or unverifiable — against the live code and the
   design doc, not against plausibility. Unverifiable claims are **discarded**, not weighed.
2. **Apply the pre-written rule** to what survives.
3. **DEFER to the owner** when the rule does not discriminate, and say precisely what
   additional evidence would.

**The judge may NOT introduce a new option.** That is what panelist three was for. A judge
that invents an answer has skipped the independence the whole procedure is built on.

### 4. Output a decision, and keep the losing arguments

Append to the design doc: the decision, the rule that produced it, and **the arguments that
lost, with their strongest points intact.** Record the decision as a dev finding in the
go-parts memory vault (see `.claude/memory-protocol.md`) so it survives the session. A panel
that ends in a summary has failed — the losing case is the most valuable artifact six months
later, when someone asks why go-parts went this way, and it is what makes the decision
reversible on evidence rather than on memory.

## Failure modes to watch for

- **Panelists converging because they read each other.** Run them blind or the result is one
  opinion wearing three hats.
- **A judge that splits the difference.** Most forks do not have a coherent midpoint;
  averaging two designs usually produces one that has neither's virtues.
- **A rule vague enough to justify anything.** If you cannot say in advance what would
  change your mind, you are not running a panel, you are collecting support.
- **Running it to diffuse responsibility.** The decision is still the owner's. The panel
  informs it; it does not launder it.

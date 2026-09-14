# Attack the Premise

**When:** Two or more fixes sharing one premise failed the same gate.

**Why:** Repeating a fix inside the same model spends time without changing the evidence.

**Pattern:**
1. State the shared premise.
2. Identify the gate that rejected each fix.
3. Test a different explanation or boundary.

**Stop:**
- Do not apply a third variation of the same failed premise without new evidence.
- If the premise is still plausible, name the observation that would distinguish it.

**Not this:** fix-root-causes traces a symptom to its cause during debugging.
attack-the-premise changes the model after repeated fixes fail the same gate.

# Test Behavior Not Implementation

**When:** Writing or keeping a test.

**Why:** Tests should protect behavior users depend on while allowing internal structure to change.

**Pattern:**
1. Name the observable behavior.
2. Exercise it through the public boundary.
3. Assert the result, error, or side effect a user can observe.

**Stop:**
- Do not assert private helpers, call counts, or incidental layout unless they are the contract.
- If a test breaks during a structure-only change, recheck whether it pins implementation.

**Not this:** prove-it-works verifies the completed artifact through the real path.
test-behavior-not-implementation chooses what an individual test is allowed to pin.

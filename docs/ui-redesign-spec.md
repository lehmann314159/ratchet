# UI Redesign Spec

## Navigation

- Rename "Dashboard" to "Current Project"
- Remove the "All Projects" component from the Current Project page
- Add an "All Projects" menu item linking to a dedicated All Projects page
- "Current Project" empty state: display "No active project" when no project is active

## Current Project page layout

Split into a left pane and a right pane (50/50).

**Left pane:** existing page content (beads table, jobs list, etc.), unchanged.

**Right pane:** static detail pane, populated by user interaction with elements in the
left pane. Empty by default until the user selects something. Does not auto-refresh —
it is a pure downstream consequence of user action and is not affected by left-pane
polling refreshes.

## Right pane content

### Bead row click → bead spec

Display the current `full_text` of the bead's active revision. This is the most
frequently needed artifact — it shows exactly what the model was asked to implement
without navigating away from the project view.

### ADJUDICATE job row click → reasoning + decision

Display `reasoning_text` and `decision` from the adjudication record for that job.
This is the most human-readable artifact in the pipeline and currently requires
navigating away to read. Surfacing it in the right pane preserves project context
while explaining why the pipeline made a particular choice.

## Deferred

- Execution trace tail (live or completed): high value but introduces polling/streaming
  complexity; revisit after the pane pattern is established.

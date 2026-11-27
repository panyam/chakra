# copilot on haiku

A walk through copilot's ladder, one step at a time: what each step adds to the agent, what it passes on 8 cases, and what it costs.

## Walkthrough

### The job

copilot sits beside a kanban board with three columns: To do, Doing and Done. The user works on the board, and the agent sees what they do, reacts, and moves cards itself. The board is a fake page in Go, with tools to read the board, move, add and rename cards, and highlight one.
Every case runs inside a host.App, as agentweb would serve the agent. The user's actions reach it as events through App.Deliver, and a trigger can start a turn nobody typed. App.Settle waits for those before the session moves on.
A case is a user session: messages, actions between turns, and actions timed to land while the agent works. It's graded on the board afterwards, on whether the agent overwrote a change the user made while it worked, and on whether the trigger fired when it should, and once.

The steps:
1. chat only
2. + page tools
3. + page events
4. + trigger
5. + critical events
6. + stale-edit guard

### 1. chat only

The App with no page tools. The agent can only talk.

**Inputs:**

- `watch` = `all`

```
  (the config is unchanged: this is the starting point)
  runs each case its own way from here (Driver)

Running 8 cases:
  FAIL   1.7s  $0.00009  Move "Plan offsite" to Doing.
         got  I can't move cards from this chat, so you'll need to drag "Plan offsite" from To do (or Done) into Doing yo...
         want C5 in Doing
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in Doin...
  FAIL   1.9s  $0.00013  Add a card "Write release notes" to To do.
         got  I can't write to the board from here, so "Write release notes" isn't in To do yet. You can add it manually,...
         want a "Write release notes" card in To do
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in Doin...
  FAIL   1.3s  $0.00008  (moves C3 to Done) Highlight the card I just finished.
         got  I can't see your board or highlight cards from here. If you tell me the card's title, I can help you find i...
         want C3 highlighted
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in Doin...
  FAIL   1.3s  $0.00010  (renames C5) Put the card I just renamed into Doing.
         got  I can't see your board or move cards from here, and I don't know which card you renamed. To move it, drag t...
         want C5 in Doing
         Board: C5 "Plan team offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in...
  FAIL   0.0s  $0.00000  (moves C2, with open items, to Done)
         got  (no answer)
         want exactly 1 firing
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C3 "Update docs" in ...
  FAIL   0.0s  $0.00000  (moves C1, all items done, to Done)
         got  (no answer)
         want exactly 1 firing
         Triggers: 0 firings
  FAIL   2.0s  $0.00012  Move every card in Doing to Done. (the user takes C2 back mid-turn)
         got  I can't move cards from here, and I don't have a view of the Doing column's contents. To move them, drag ea...
         want C1 and C3 in Done, C2 left in To do
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in Doin...
  FAIL   2.4s  $0.00015  Rename C7 to "Q3 launch plan". (the user renames it as the agent does)
         got  I can't confirm that C7 has been renamed, since I don't have write access to the board from here. To make t...
         want C7 keeps the user's title "Launch plan v2"
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in Doin...

chat only passed 0 of 8.
This step cost $0.00067, and the walk has spent $0.00067.
The next step runs the same 8 cases, so expect about $0.00067 more.
```

### 2. + page tools

Tools to read the board and change it, and a line in the prompt saying to read before changing.

**Inputs:**

- `watch` = `all`

```
  + instructions:
      You can read the board and change it with your tools. Read the board before you change it.
  + tools: read_board, move_card, add_card, rename_card, highlight

Running 8 cases:
  PASS   2.1s  $0.00038  Move "Plan offsite" to Doing.
         got  "Plan offsite" is now in Doing.
         want C5 in Doing
  PASS   2.0s  $0.00039  Add a card "Write release notes" to To do.
         got  Added "Write release notes" to To do.
         want a "Write release notes" card in To do
  FAIL   2.4s  $0.00034  (moves C3 to Done) Highlight the card I just finished.
         got  I can't tell which card you just finished. Did you mean one of these? | - **C8 "Set up CI"** (Done) | - **C...
         want C3 highlighted
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in Doin...
  FAIL   2.4s  $0.00034  (renames C5) Put the card I just renamed into Doing.
         got  I can't tell which card you renamed. The board doesn't show recent edits. Which one is it? | - **C5** Plan ...
         want C5 in Doing
         Board: C5 "Plan team offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in...
  FAIL   0.0s  $0.00000  (moves C2, with open items, to Done)
         got  (no answer)
         want exactly 1 firing
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C3 "Update docs" in ...
  FAIL   0.0s  $0.00000  (moves C1, all items done, to Done)
         got  (no answer)
         want exactly 1 firing
         Triggers: 0 firings
  FAIL   2.5s  $0.00050  Move every card in Doing to Done. (the user takes C2 back mid-turn)
         got  Moved all three Doing cards to Done: Fix login, Ship v2, and Update docs. | Ship v2 still has 2 open checkl...
         want no user edit overwritten
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C8 "Set up CI" in Done, C3 "Update docs" in D...
  FAIL   2.2s  $0.00039  Rename C7 to "Q3 launch plan". (the user renames it as the agent does)
         got  C7 is now "Q3 launch plan".
         want no user edit overwritten
         Board: C5 "Plan offsite" in To do, C7 "Q3 launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in D...

+ page tools passed 2 of 8, +2 on the step before.
This step cost $0.00233, and the walk has spent $0.00300.
The next step runs the same 8 cases, so expect about $0.00233 more.
```

### 3. + page events

The page sends each user action to the App with App.Deliver. The host's injection policy puts it in the next turn's context, as a sentence the event's hint templates.

**Inputs:**

- `watch` = `all`

```
  (the config is unchanged: this is the starting point)
  runs each case its own way from here (Driver)

Running 8 cases:
  PASS   2.0s  $0.00038  Move "Plan offsite" to Doing.
         got  "Plan offsite" is now in Doing.
         want C5 in Doing
  PASS   1.9s  $0.00039  Add a card "Write release notes" to To do.
         got  Added "Write release notes" to To do.
         want a "Write release notes" card in To do
  PASS   1.9s  $0.00037  (moves C3 to Done) Highlight the card I just finished.
         got  I've highlighted C3 "Update docs" in Done.
         want C3 highlighted
  PASS   2.0s  $0.00041  (renames C5) Put the card I just renamed into Doing.
         got  Moved C5 "Plan team offsite" to Doing.
         want C5 in Doing
  FAIL   0.0s  $0.00000  (moves C2, with open items, to Done)
         got  (no answer)
         want exactly 1 firing
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C3 "Update docs" in ...
  FAIL   0.0s  $0.00000  (moves C1, all items done, to Done)
         got  (no answer)
         want exactly 1 firing
         Triggers: 0 firings
  FAIL   2.7s  $0.00050  Move every card in Doing to Done. (the user takes C2 back mid-turn)
         got  I moved the three Doing cards to Done: Fix login, Ship v2, and Update docs. | Ship v2 still has open checkl...
         want no user edit overwritten
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C8 "Set up CI" in Done, C3 "Update docs" in D...
  FAIL   2.1s  $0.00039  Rename C7 to "Q3 launch plan". (the user renames it as the agent does)
         got  C7 is now "Q3 launch plan".
         want no user edit overwritten
         Board: C5 "Plan offsite" in To do, C7 "Q3 launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in D...

+ page events passed 4 of 8, +2 on the step before.
This step cost $0.00245, and the walk has spent $0.00544.
The next step runs the same 8 cases, so expect about $0.00245 more.
```

### 4. + trigger

A trigger on card.moved with to=Done starts a turn by itself: check the card's checklist, and move it back if items are open.

**Inputs:**

- `watch` = `all`

```
  (the config is unchanged: this is the starting point)
  runs each case its own way from here (Driver)

Running 8 cases:
  PASS   1.8s  $0.00038  Move "Plan offsite" to Doing.
         got  "Plan offsite" is now in Doing.
         want C5 in Doing
  PASS   2.0s  $0.00039  Add a card "Write release notes" to To do.
         got  Added "Write release notes" to the To do column.
         want a "Write release notes" card in To do
  PASS   2.6s  $0.00055  (moves C3 to Done) Highlight the card I just finished.
         got  I've highlighted C3 (Update docs) in Done.
         want C3 highlighted
  PASS   2.2s  $0.00041  (renames C5) Put the card I just renamed into Doing.
         got  C5 "Plan team offsite" is now in Doing.
         want C5 in Doing
  PASS   2.3s  $0.00045  (moves C2, with open items, to Done)
         got  I moved C2 (Ship v2) back to Doing and highlighted it. It's checklist is 1/3, with these items still open: ...
         want C2 back in Doing and highlighted
  PASS   1.6s  $0.00027  (moves C1, all items done, to Done)
         got  Fix login (C1) is in Done and its checklist is complete (2/2), so it stays where it is.
         want C1 left in Done
  FAIL   2.4s  $0.00049  Move every card in Doing to Done. (the user takes C2 back mid-turn)
         got  Moved all three Doing cards to Done: Fix login, Ship v2, and Update docs. Note that Ship v2 still has open ...
         want no user edit overwritten
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C8 "Set up CI" in Done, C3 "Update docs" in D...
  FAIL   2.0s  $0.00040  Rename C7 to "Q3 launch plan". (the user renames it as the agent does)
         got  Renamed C7 to "Q3 launch plan".
         want no user edit overwritten
         Board: C5 "Plan offsite" in To do, C7 "Q3 launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in D...

+ trigger passed 6 of 8, +2 on the step before.
This step cost $0.00334, and the walk has spent $0.00878.
The next step runs the same 8 cases, so expect about $0.00334 more.
```

### 5. + critical events

The host injects events at turn start. The page now marks its events priority critical, so the host also passes one the user sends mid-turn before the agent's next model call.

**Inputs:**

- `watch` = `all`

```
  (the config is unchanged: this is the starting point)
  runs each case its own way from here (Driver)

Running 8 cases:
  PASS   1.9s  $0.00038  Move "Plan offsite" to Doing.
         got  "Plan offsite" is now in Doing.
         want C5 in Doing
  PASS   2.1s  $0.00039  Add a card "Write release notes" to To do.
         got  Added "Write release notes" to To do.
         want a "Write release notes" card in To do
  PASS   2.5s  $0.00055  (moves C3 to Done) Highlight the card I just finished.
         got  I've highlighted C3 (Update docs) in Done.
         want C3 highlighted
  PASS   2.3s  $0.00042  (renames C5) Put the card I just renamed into Doing.
         got  C5 "Plan team offsite" is now in Doing.
         want C5 in Doing
  PASS   2.1s  $0.00047  (moves C2, with open items, to Done)
         got  Moved C2 (Ship v2) back to Doing and highlighted it. It's not finished: its checklist is 1/3, with these it...
         want C2 back in Doing and highlighted
  PASS   1.5s  $0.00027  (moves C1, all items done, to Done)
         got  "Fix login" (C1) has its checklist complete (2/2), so it stays in Done. No changes made.
         want C1 left in Done
  PASS   4.8s  $0.00081  Move every card in Doing to Done. (the user takes C2 back mid-turn)
         got  Moved "Fix login" (C1) and "Update docs" (C3) to Done. "Ship v2" (C2) was already moved to To do, so I left...
         want C1 and C3 in Done, C2 left in To do
  FAIL   3.9s  $0.00073  Rename C7 to "Q3 launch plan". (the user renames it as the agent does)
         got  C7 is now titled "Q3 launch plan", which is the name you asked for. You mentioned a rename to "Launch plan ...
         want no user edit overwritten
         Board: C5 "Plan offsite" in To do, C7 "Q3 launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in D...

+ critical events passed 7 of 8, +1 on the step before.
This step cost $0.00403, and the walk has spent $0.01281.
The next step runs the same 8 cases, so expect about $0.00403 more.
```

### 6. + stale-edit guard

Tool middleware refuses an edit to a card the user changed after the agent last read the board, and tells the agent what changed. The refusal is a free result, so it costs no step.

**Inputs:**

- `watch` = `all`

```
  + 1 tool middleware

Running 8 cases:
  PASS   1.8s  $0.00038  Move "Plan offsite" to Doing.
         got  "Plan offsite" is now in Doing.
         want C5 in Doing
  PASS   1.9s  $0.00039  Add a card "Write release notes" to To do.
         got  Added "Write release notes" to the To do column.
         want a "Write release notes" card in To do
  PASS   2.6s  $0.00056  (moves C3 to Done) Highlight the card I just finished.
         got  I've highlighted C3 (Update docs) in Done.
         want C3 highlighted
  PASS   2.0s  $0.00041  (renames C5) Put the card I just renamed into Doing.
         got  C5 "Plan team offsite" is now in Doing.
         want C5 in Doing
  PASS   2.3s  $0.00046  (moves C2, with open items, to Done)
         got  Ship v2 (C2) had open checklist items, so I moved it back to Doing and highlighted it. Still open: "Write n...
         want C2 back in Doing and highlighted
  PASS   1.3s  $0.00027  (moves C1, all items done, to Done)
         got  "Fix login" (C1) is in Done and its checklist is complete (2/2), so it stays where it is.
         want C1 left in Done
  PASS   4.1s  $0.00075  Move every card in Doing to Done. (the user takes C2 back mid-turn)
         got  I moved Fix login (C1) and Update docs (C3) to Done. Ship v2 (C2) was already moved to To do, so I left it ...
         want C1 and C3 in Done, C2 left in To do
  PASS   3.9s  $0.00057  Rename C7 to "Q3 launch plan". (the user renames it as the agent does)
         got  C7 is now "Launch plan v2", which you renamed. Do you want me to rename it to "Q3 launch plan" anyway, repl...
         want C7 keeps the user's title "Launch plan v2"

+ stale-edit guard passed 8 of 8, +1 on the step before.
This step cost $0.00378, and the walk has spent $0.01659.
```

### 7. The table

```
copilot: 8 cases
step                pass  change  usd/correct  sec/correct  
chat only           0/8           -            -            
+ page tools        2/8   +2      $0.0012      6.80         
+ page events       4/8   +2      $0.00061     3.14         
+ trigger           6/8   +2      $0.00056     2.81         
+ critical events   7/8   +1      $0.00058     3.04         
+ stale-edit guard  8/8   +1      $0.00047     2.47         
```


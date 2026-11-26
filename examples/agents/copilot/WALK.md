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
5. + mid-turn notes
6. + stale-edit guard

### 1. chat only

The App with no page tools. The agent can only talk.

**Inputs:**

- `watch` = `all`

```
  (the config is unchanged: this is the starting point)
  runs each case its own way from here (Driver)

Running 8 cases:
  FAIL   1.6s  $0.00010  Move "Plan offsite" to Doing.
         got  I can't move cards from this chat. To do it yourself, drag "Plan offsite" from To do into Doing, or open th...
         want C5 in Doing
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in Doin...
  FAIL   2.2s  $0.00015  Add a card "Write release notes" to To do.
         got  I can't change the board from this chat, so "Write release notes" hasn't been added to To do yet. You can a...
         want a "Write release notes" card in To do
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in Doin...
  FAIL   1.3s  $0.00008  (moves C3 to Done) Highlight the card I just finished.
         got  I can't see your board or highlight cards from here. If you tell me the card's name, I can help you find it...
         want C3 highlighted
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in Doin...
  FAIL   1.1s  $0.00008  (renames C5) Put the card I just renamed into Doing.
         got  I can't see your board or move cards from here. To move it, drag the card into the **Doing** column, or ope...
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
  FAIL   1.7s  $0.00011  Move every card in Doing to Done. (the user takes C2 back mid-turn)
         got  I can't move cards from here, and I can't see what's currently in Doing. To do it yourself, drag each card ...
         want C1 and C3 in Done, C2 left in To do
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in Doin...
  FAIL   1.6s  $0.00010  Rename C7 to "Q3 launch plan". (the user renames it as the agent does)
         got  I can't edit cards directly from here, so you'll need to rename C7 yourself. Open the card, click its title...
         want C7 keeps the user's title "Launch plan v2"
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in Doin...

chat only passed 0 of 8.
This step cost $0.00062, and the walk has spent $0.00062.
The next step runs the same 8 cases, so expect about $0.00062 more.
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
  PASS   1.8s  $0.00038  Move "Plan offsite" to Doing.
         got  "Plan offsite" is now in Doing.
         want C5 in Doing
  PASS   2.2s  $0.00039  Add a card "Write release notes" to To do.
         got  Added "Write release notes" to To do.
         want a "Write release notes" card in To do
  FAIL   1.9s  $0.00030  (moves C3 to Done) Highlight the card I just finished.
         got  I can't tell which card you just finished. Two cards are in Done: **Set up CI** (C8) and **Update docs** (C...
         want C3 highlighted
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in Doin...
  FAIL   1.9s  $0.00031  (renames C5) Put the card I just renamed into Doing.
         got  I can't tell which card you renamed. The board doesn't show recent edits. Which card should I move to Doing...
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
  FAIL   2.1s  $0.00049  Move every card in Doing to Done. (the user takes C2 back mid-turn)
         got  Moved all three Doing cards to Done: Fix login, Ship v2, and Update docs. Note that Ship v2 still has open ...
         want no user edit overwritten
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C8 "Set up CI" in Done, C3 "Update docs" in D...
  FAIL   2.1s  $0.00039  Rename C7 to "Q3 launch plan". (the user renames it as the agent does)
         got  Renamed C7 to "Q3 launch plan".
         want no user edit overwritten
         Board: C5 "Plan offsite" in To do, C7 "Q3 launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in D...

+ page tools passed 2 of 8, +2 on the step before.
This step cost $0.00225, and the walk has spent $0.00288.
The next step runs the same 8 cases, so expect about $0.00225 more.
```

### 3. + page events

The page sends each user action to the App with App.Deliver. The host's injection policy puts it in the next turn's context, as a sentence the event's hint templates.

**Inputs:**

- `watch` = `all`

```
  (the config is unchanged: this is the starting point)
  runs each case its own way from here (Driver)

Running 8 cases:
  PASS   1.9s  $0.00038  Move "Plan offsite" to Doing.
         got  "Plan offsite" is now in Doing.
         want C5 in Doing
  PASS   1.9s  $0.00039  Add a card "Write release notes" to To do.
         got  Added "Write release notes" to To do.
         want a "Write release notes" card in To do
  PASS   2.0s  $0.00037  (moves C3 to Done) Highlight the card I just finished.
         got  I've highlighted C3, "Update docs," in Done.
         want C3 highlighted
  PASS   2.2s  $0.00041  (renames C5) Put the card I just renamed into Doing.
         got  C5 "Plan team offsite" is now in Doing.
         want C5 in Doing
  FAIL   0.0s  $0.00000  (moves C2, with open items, to Done)
         got  (no answer)
         want exactly 1 firing
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C1 "Fix login" in Doing, C3 "Update docs" in ...
  FAIL   0.0s  $0.00000  (moves C1, all items done, to Done)
         got  (no answer)
         want exactly 1 firing
         Triggers: 0 firings
  FAIL   2.1s  $0.00049  Move every card in Doing to Done. (the user takes C2 back mid-turn)
         got  Moved all three Doing cards to Done: Fix login, Ship v2, and Update docs. Note that Ship v2 still has open ...
         want no user edit overwritten
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C8 "Set up CI" in Done, C1 "Fix login" in Don...
  FAIL   2.2s  $0.00039  Rename C7 to "Q3 launch plan". (the user renames it as the agent does)
         got  Renamed C7 to "Q3 launch plan".
         want no user edit overwritten
         Board: C5 "Plan offsite" in To do, C7 "Q3 launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in D...

+ page events passed 4 of 8, +2 on the step before.
This step cost $0.00243, and the walk has spent $0.00531.
The next step runs the same 8 cases, so expect about $0.00243 more.
```

### 4. + trigger

A trigger on card.moved with to=Done starts a turn by itself: check the card's checklist, and move it back if items are open.

**Inputs:**

- `watch` = `all`

```
  (the config is unchanged: this is the starting point)
  runs each case its own way from here (Driver)

Running 8 cases:
  PASS   2.3s  $0.00067  Move "Plan offsite" to Doing.
         got  Moved "Plan offsite" to Doing.
         want C5 in Doing
  PASS   2.0s  $0.00068  Add a card "Write release notes" to To do.
         got  Added "Write release notes" to the To do column.
         want a "Write release notes" card in To do
  PASS   2.7s  $0.00093  (moves C3 to Done) Highlight the card I just finished.
         got  I've highlighted C3 (Update docs) in Done.
         want C3 highlighted
  PASS   2.1s  $0.00070  (renames C5) Put the card I just renamed into Doing.
         got  C5 "Plan team offsite" is now in Doing.
         want C5 in Doing
  PASS   2.1s  $0.00074  (moves C2, with open items, to Done)
         got  I moved C2 (Ship v2) back to Doing and highlighted it. It's checklist is 1/3, with these items still open: ...
         want C2 back in Doing and highlighted
  PASS   1.3s  $0.00045  (moves C1, all items done, to Done)
         got  C1 (Fix login) is in Done and its checklist is complete (2/2), so I've left it where it is.
         want C1 left in Done
  FAIL   2.0s  $0.00077  Move every card in Doing to Done. (the user takes C2 back mid-turn)
         got  Moved all three Doing cards to Done: Fix login, Ship v2, and Update docs. Note that Ship v2 still has two o...
         want no user edit overwritten
         Board: C5 "Plan offsite" in To do, C7 "Launch plan" in To do, C8 "Set up CI" in Done, C3 "Update docs" in D...
  FAIL   4.3s  $0.00070  Rename C7 to "Q3 launch plan". (the user renames it as the agent does)
         got  C7 is now "Q3 launch plan".
         want no user edit overwritten
         Board: C5 "Plan offsite" in To do, C7 "Q3 launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in D...

+ trigger passed 6 of 8, +2 on the step before.
This step cost $0.00563, and the walk has spent $0.01094.
The next step runs the same 8 cases, so expect about $0.00563 more.
```

### 5. + mid-turn notes

The host injects events at turn start only. A session step stage tells the agent, before each model call, what the user changed since the turn began.

**Inputs:**

- `watch` = `all`

```
  + 1 step stage(s), run before each model call

Running 8 cases:
  PASS   1.8s  $0.00066  Move "Plan offsite" to Doing.
         got  "Plan offsite" is now in Doing.
         want C5 in Doing
  PASS   1.9s  $0.00068  Add a card "Write release notes" to To do.
         got  Added "Write release notes" to To do.
         want a "Write release notes" card in To do
  PASS   2.6s  $0.00094  (moves C3 to Done) Highlight the card I just finished.
         got  I've highlighted C3 (Update docs) in Done.
         want C3 highlighted
  PASS   2.3s  $0.00069  (renames C5) Put the card I just renamed into Doing.
         got  C5 "Plan team offsite" is now in Doing.
         want C5 in Doing
  PASS   2.3s  $0.00074  (moves C2, with open items, to Done)
         got  I moved C2 (Ship v2) back to Doing and highlighted it. Its checklist is 1/3, with these items still open: |...
         want C2 back in Doing and highlighted
  PASS   1.4s  $0.00045  (moves C1, all items done, to Done)
         got  C1 (Fix login) has a complete checklist (2/2), so it stays in Done. No changes made.
         want C1 left in Done
  PASS   2.9s  $0.00085  Move every card in Doing to Done. (the user takes C2 back mid-turn)
         got  Moved C1 (Fix login) and C3 (Update docs) to Done. I left C2 (Ship v2) alone since you moved it to To do.
         want C1 and C3 in Done, C2 left in To do
  FAIL   4.7s  $0.00115  Rename C7 to "Q3 launch plan". (the user renames it as the agent does)
         got  C7 is now "Q3 launch plan." My rename went through, but the board doesn't show the "Launch plan v2" title y...
         want no user edit overwritten
         Board: C5 "Plan offsite" in To do, C7 "Q3 launch plan" in To do, C1 "Fix login" in Doing, C2 "Ship v2" in D...

+ mid-turn notes passed 7 of 8, +1 on the step before.
This step cost $0.00615, and the walk has spent $0.01709.
The next step runs the same 8 cases, so expect about $0.00615 more.
```

### 6. + stale-edit guard

Tool middleware refuses an edit to a card the user changed after the agent last read the board, and tells the agent what changed. The refusal is a free result, so it costs no step.

**Inputs:**

- `watch` = `all`

```
  + 1 tool middleware

Running 8 cases:
  PASS   1.9s  $0.00066  Move "Plan offsite" to Doing.
         got  "Plan offsite" is now in Doing.
         want C5 in Doing
  PASS   2.0s  $0.00068  Add a card "Write release notes" to To do.
         got  Added "Write release notes" to To do.
         want a "Write release notes" card in To do
  PASS   2.8s  $0.00095  (moves C3 to Done) Highlight the card I just finished.
         got  I've highlighted C3 (Update docs) in Done.
         want C3 highlighted
  PASS   2.2s  $0.00069  (renames C5) Put the card I just renamed into Doing.
         got  C5 "Plan team offsite" is now in Doing.
         want C5 in Doing
  PASS   2.2s  $0.00076  (moves C2, with open items, to Done)
         got  I moved C2 (Ship v2) back to Doing and highlighted it. It's checklist is 1/3, with these items still open: ...
         want C2 back in Doing and highlighted
  PASS   1.4s  $0.00045  (moves C1, all items done, to Done)
         got  C1 (Fix login) has its checklist complete (2/2), so it stays in Done. No changes made.
         want C1 left in Done
  PASS   4.2s  $0.00119  Move every card in Doing to Done. (the user takes C2 back mid-turn)
         got  Moved C1 (Fix login) and C3 (Update docs) from Doing to Done. C2 (Ship v2) was already moved to To do by yo...
         want C1 and C3 in Done, C2 left in To do
  PASS   5.9s  $0.00127  Rename C7 to "Q3 launch plan". (the user renames it as the agent does)
         got  C7 is currently "Launch plan v2", so my rename didn't go through. Should I still rename it to "Q3 launch pl...
         want C7 keeps the user's title "Launch plan v2"

+ stale-edit guard passed 8 of 8, +1 on the step before.
This step cost $0.00665, and the walk has spent $0.02374.
```

### 7. The table

```
copilot: 8 cases
step                pass  change  usd/correct  sec/correct  
chat only           0/8           -            -            
+ page tools        2/8   +2      $0.0011      5.95         
+ page events       4/8   +2      $0.00061     3.07         
+ trigger           6/8   +2      $0.00094     3.13         
+ mid-turn notes    7/8   +1      $0.00088     2.86         
+ stale-edit guard  8/8   +1      $0.00083     2.80         
```


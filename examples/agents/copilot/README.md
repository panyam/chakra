# copilot

A co-pilot beside a kanban board (#359). The user works on the board, and
the agent sees what they do, reacts, and moves cards itself. Every case runs
inside a `host.App`, as agentweb would serve it. So the user's actions reach
the agent as events through `App.Deliver`, and a trigger can start a turn
nobody typed.

```
cd examples/agents/copilot
make tui                 # walk the ladder on the scripted model
make note LIVE=haiku     # on a real model
make exercise            # the whole ladder, table at the end
```

## The page

The board is a fake page in Go (`board.go`), reset for each case. It has
three columns, To do, Doing and Done, and six cards with checklists. C2,
"Ship v2", has two open items. C1 and C3 are complete.

- **Tools:** `read_board`, `move_card`, `add_card`, `rename_card` and
  `highlight`.
- **User actions:** the page moves or renames a card for the user, and from
  the third step sends each action to the App as an event, `card.moved` or
  `card.renamed`. The event's hint templates it as one sentence, such as
  "The user moved C3 (Update docs) from Doing to Done."
- **The clock:** an action can be timed to land just before or just after
  the agent's nth call of a tool. Middleware plays it, ahead of any step's
  middleware, so "before `rename_card`" lands after the model chose the call
  and before anything checks it.

## The sessions

A case is a user session: what the user says, what they do between turns,
and what they do while the agent works. The driver plays it through the App,
and calls `App.Settle` after each beat so a proactive turn finishes before
the next one starts.

| session | passes when |
|---|---|
| "Move "Plan offsite" to Doing." | C5 is in Doing |
| "Add a card "Write release notes" to To do." | the card is in To do |
| moves C3 to Done, then "Highlight the card I just finished." | C3 is highlighted |
| renames C5, then "Put the card I just renamed into Doing." | C5 is in Doing |
| moves C2, with open items, to Done, and says nothing | the trigger fired once, and C2 is back in Doing and highlighted |
| moves C1, all items done, to Done | the trigger fired once, and C1 is still in Done |
| "Move every card in Doing to Done.", and takes C2 back to To do just after the agent reads the board | C1 and C3 in Done, C2 in To do |
| "Rename C7 to "Q3 launch plan".", and renames C7 to "Launch plan v2" as the agent's rename goes out | C7 keeps the user's title |

Every case also fails if the agent edited a field the user changed while the
agent was working, or if the trigger fired more often than the user moved a
card to Done.

## The steps

1. **chat only.** The App with no page tools.
2. **+ page tools.** The tools, and a line saying to read the board first.
3. **+ page events.** The page calls `App.Deliver` for each user action. The
   host's injection policy puts the event in the next turn's context.
4. **+ trigger.** A host trigger on `card.moved` with `to` = Done starts a
   turn by itself, with instructions to check the card's checklist.
5. **+ critical events.** The page marks each event `priority: critical`.
   Other events wait for the next turn's start. The host passes a critical
   one to the agent before its next model call, mid-turn included, through
   its `host.critical-events` step stage.
6. **+ stale-edit guard.** Tool middleware refuses an edit to a card the user
   changed after the agent last read the board, and says what changed. The
   refusal is a free result, so it costs no step. This is #282's question
   about a world changing under the agent. For a page, the existing
   middleware hook answered it, with no new core hook.

Steps 1, 3, 4 and 5 change the host, not the Runner, so they set a step
`Driver`. The driver builds the App from the step's `RunnerConfig`: the
tools, middleware, step stages and turn-end hooks go in as an extension.

## The table

On the scripted model CI runs:

```
$ make exercise
copilot: 8 cases
step                pass  change  usd/correct  sec/correct
chat only           0/8           -            -
+ page tools        2/8   +2      -            0.00
+ page events       4/8   +2      -            0.00
+ trigger           6/8   +2      -            0.00
+ critical events   7/8   +1      -            0.00
+ stale-edit guard  8/8   +1      -            0.00
```

The scripted agent knows what the user just did only from an event note. It
never checks a card in Done on its own. It plans from the board it read. And
it doesn't re-read just before an edit. Each step closes one of those gaps,
so the numbers show the steps are wired, and say nothing about a real model.

## A live run

On Claude Haiku 5.5, on 2026-11-26. [WALK.md](WALK.md) is the recorded walk,
and `make walk-md LIVE=haiku` re-renders it from `walk-haiku.json` with no
model calls.

```
step                pass  change  usd/correct  sec/correct
chat only           0/8           -            -
+ page tools        2/8   +2      $0.0012      6.80
+ page events       4/8   +2      $0.00061     3.14
+ trigger           6/8   +2      $0.00056     2.81
+ critical events   7/8   +1      $0.00058     3.04
+ stale-edit guard  8/8   +1      $0.00047     2.47
```

Haiku climbs the same as the scripted model, and each step fixes the case
it was built for:

- With tools and no events, Haiku asked rather than guessed: "I can't tell
  which card you just finished. Two cards are in Done." The event note
  answered that.
- Without the trigger, a card dropped in Done with open items is left there,
  because no turn runs.
- Without critical events, Haiku moved all three Doing cards, C2 included,
  which the user had just taken back. With them: "Ship v2 (C2) was already
  moved to To do, so I left it."
- Without the guard, Haiku's rename landed on top of the user's. With it: "C7
  is now "Launch plan v2", which you renamed. Do you want me to rename it to
  "Q3 launch plan" anyway?"
- The walk cost $0.017.

## What the build found

- In-process code had no way to know an event it passed to `App.Deliver`
  had been handled, so the next beat raced the proactive turn. `App.Settle`
  now waits for every delivered event and any turn it started.
- The host injected events at turn start only, so the first version of step
  5 was a stage in this example. Now the host passes an event marked
  `priority: critical` before each model call (#383).
- One trigger in config used to give the model the host's eight meta-tools,
  such as `create_trigger`, beside the page tools. Now only `metaTools:
  true` offers them (#383).
- `App.Deliver` only queues an event. A user action timed for the gap
  between two model calls could reach the host after the second call began,
  so the page waits on `App.Settle` after each action.

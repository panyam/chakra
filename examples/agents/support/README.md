# support

A customer-support agent for Threadline, a small online shop (#354). It
answers from six policy pages in `docs/`, and acts on an orders database
through tools. A model plays each customer from a goal, the way τ²-bench's
simulated user does, so a conversation runs until the customer is satisfied,
refused or handed to a person.

```
cd examples/agents/support
make tui                 # walk the ladder on the scripted model
make note LIVE=haiku     # on a real model, which also plays the customers
make exercise            # the whole ladder, table at the end
```

## The shop

- **Policy pages** (`docs/`): returns, refunds, shipping, address, warranty,
  and when to hand off to a person.
- **Orders** (`shop.go`): six orders, reset for each case. A1 can be
  refunded. A2 was delivered 45 days ago. A3 was a final sale. A4 is still
  processing, and A5 has shipped. A6 is a kettle.
- **Tools:** `lookup_order`, `refund_order`, `change_address`, and from the
  fifth step `escalate_to_human`.

## The customers

Each case is an opening message and a goal. `eval.ModelUser` plays the
customer from the goal on the same model as the agent, and says DONE when
the goal is met or refused.

| customer | wants | passes when |
|---|---|---|
| Ana | a refund for A1 | A1 is refunded |
| Ben | a refund for A2, 45 days after delivery | A2 isn't refunded |
| Cal | a refund for A3, a final sale | A3 isn't refunded |
| Dee | A4 sent to a new address | A4's address changed |
| Eve | A5 sent to a new address, after it shipped | A5's address didn't change |
| (no order) | how long returns last | a reply says 30 days and cites `[returns]` |
| (no order) | how long the warranty on headphones lasts | a reply says one year and cites `[warranty]` |
| Fin | something done about a sparking kettle, with a lawyer | the agent handed off to a person |

A customer often says thanks after getting an answer, so the agent's last
reply is "You're welcome". The questions are graded on every reply the agent
made, which the case's `User` keeps as it sees the conversation.

## The steps

1. **tools + policy.** The shop's tools, and a two-line policy summary in the
   prompt. The pages themselves aren't available.
2. **+ docs.** A retrieval stage finds the two pages that share the most
   words with the customer's message, and puts them before the model's call.
   It's a keyword match, which is plenty for six short pages. #11, a
   `VectorStore`, waits for an agent whose documents need embeddings.
3. **+ citations.** The prompt asks for a `[page]` on any policy it states,
   and a turn-end hook sends back an answer that cites nothing (a question
   back to the customer is let through).
4. **+ policy guard.** Tool middleware refuses a refund or an address change
   the policy doesn't allow, before it reaches the database, and tells the
   model why. The issue suggested a turn-end gate, but by the end of a turn
   the refund would already have gone through.
5. **+ handoff.** `escalate_to_human`, and a rule for when to use it.

## The table

On the scripted model CI runs, which plays both the agent and the customer:

```
$ make exercise
support: 8 cases
step            pass  change  usd/correct  sec/correct
tools + policy  2/8           -            0.00
+ docs          3/8   +1      -            0.00
+ citations     4/8   +1      -            0.00
+ policy guard  7/8   +3      -            0.00
+ handoff       8/8   +1      -            0.00
```

The scripted agent doesn't know the return window or the warranty without
the pages. It cites the warranty page unprompted and the returns page only
once asked. It never checks the policy before acting, so only the guard
stops the three refunds and address changes the policy doesn't allow. And
it can't hand off until it has the tool. So the numbers show the steps are
wired, and say nothing about a real model.

## A live run

On Claude Haiku 5.5, playing both the agent and the customers, on
2026-11-25. [WALK.md](WALK.md) is the recorded walk, and `make walk-md
LIVE=haiku` re-renders it from `walk-haiku.json` with no model calls.

```
step            pass  change  usd/correct  sec/correct
tools + policy  5/8           $0.0013      13.18
+ docs          5/8   ~+0     $0.0016      14.88
+ citations     7/8   ~+2     $0.0014      12.05
+ policy guard  7/8   ~+0     $0.0016      14.58
+ handoff       8/8   ~+1     $0.0017      13.06

~ one run of a live model shows no spread, so the change may be noise. Running each case more than once measures it.
```

- Haiku follows the two-line policy summary from the first step. It turned
  down Ben's late refund, Cal's final sale and Eve's address change without
  the guard, so the guard adds no passes here. It's still the one step that
  doesn't depend on the model behaving.
- The pages alone add nothing. With them, Haiku got the warranty right,
  which it couldn't before, but it didn't cite either page until the third
  step asked it to. So citations add two.
- Fin, with the sparking kettle and the lawyer, is only handed off once
  there's a tool to do it. Before that, Haiku told him it couldn't transfer
  him.
- The walk cost $0.05, customers included. The customer model's tokens
  aren't counted in `usd/correct`, as a judge's aren't.

## What the live runs found in eval

The first two live walks turned up three bugs, all fixed in this PR:

- `ModelUser` ended a conversation only on an exact `DONE`. Haiku replied
  "DONE" followed by a line of thanks, which went to the agent as a message.
- Anthropic's API wants a conversation's roles to alternate, starting and
  ending with the user's. A refused answer and its retry are two agent
  replies in a row, and the customer model got an HTTP 400.
- Only the agent's last reply was graded, so an answer followed by "thanks"
  read as a miss.

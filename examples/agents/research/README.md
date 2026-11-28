# research

An agent that researches a question across sources and answers with
citations (#355). Research is the second most common use case in LangChain's
2026 survey of agent builders. This one answers questions about Shetland's islands and
lighthouses from snapshots of real pages, so every run sees the same pages and
CI needs no network.

## The sources

`testdata/pages` holds 81 pages as plain text, from three sources:

| source | pages | what it holds |
|---|---|---|
| wikipedia | 64 | current Wikipedia pages on Shetland's islands and lighthouses, the Stevenson engineers and the Fair Isle ferry, and on other Scottish islands and lighthouses that a search turns up beside them |
| wikivoyage | 10 | current Wikivoyage pages: ferries, flights and fares |
| archive | 7 | Wikipedia pages as they stood at the end of 2015, before the 2022 census |

Each file has a header with its URL, title, source and revision, and then the
page's text, cut before its references. `testdata/SOURCES.md` links every
revision, and the text is under CC BY-SA 4.0. An archive page's URL is the
real `index.php?oldid=` link for that revision.

Two tools read them, and both run in the same process:

| tool | does |
|---|---|
| `search(query, source?)` | the five best pages for the query's words, each a title, a URL and a source |
| `fetch(url, offset?)` | 2,000 characters of the page from `offset`, and where the next part starts. Any URL outside the snapshots gets an error |

`search` weights each word by how rare it is across the pages. It is a
small stand-in for a web search engine. The first calibration runs had
`search` return the sentence that matched best, and `fetch` the whole page.
Haiku 5.5 then passed 19 of 20 with the bare step, so no step could show it
helping. Search without snippets, pages in parts, and the pages that look
relevant but aren't make a model read before it answers.

## The eval

Twenty questions in `questions.go`, in the style of GAIA. Each has a short
answer, and two scorers grade it:

- **Answer** passes when the answer names one of the accepted forms as whole
  words, with its URLs taken out. For a question that names two choices, such
  as "Sumburgh Head or Muckle Flugga", the right one has to come first.
- **Cites** passes when every URL the answer cites is a page in the snapshots,
  and at least one of them supports the answer.

These trip a model:

- **A page in between.** "The engineer who built the Sule Skerry lighthouse
  had an uncle who designed a shelter for meteorological instruments. What
  is that shelter called?" The Sule Skerry page doesn't name the engineer.
  David Alan Stevenson's page lists Sule Skerry among his lighthouses, and
  names his uncle Thomas. Thomas Stevenson's page names the Stevenson screen.
- **Sums across pages.** "How old was David Stevenson on the day Muckle
  Flugga Lighthouse was first lit?" He was born on 11 January 1815, and the
  light was first lit on 1 January 1858. That's 42, not 43.
- **An older revision.** "What was the population of Foula at the most recent
  census?" The 2015 revision says 38 people. The current page says 17 at the
  2022 census.

## The steps

| step | hook | adds |
|---|---|---|
| search + fetch | tools | the two tools, and a prompt asking for a short answer and its URLs |
| + citation check | turn end | refuses an answer that cites nothing, or cites a URL the turn never fetched |
| + plan slot | each model call | a `plan` slot the model writes with `write_slot`: the sub-questions, and each fact with its URL. The slot goes before every model call |
| + sub-agents | each tool call | `search` gives way to `research`, a `FanOutSource` that sends one task to three sub-agents at once, one for each source |
| + critic | turn end | a second call to the model reads the cited pages, and refuses an answer they don't support |
| + cost cap | each model call | a `TreeBudget` of 300,000 tokens per question, counted across the sub-agents too. In the calibration run on Haiku, the middle question took about 150,000 tokens and the costliest about 1,100,000, so the cap cuts off only the three longest |

The sub-agents and the critic call the model outside the turn's own Runner,
so the turn's `Usage` leaves them out. The ladder counts them through a
meter on their provider, and each case's `eval.Setup.Spent` reports it. The
cost column includes them.

## What each step changed

Gemini 3.5 Flash-Lite, each case once, with `make exercise
LIVE=gemini-flash-lite`. The connection has no price, so the cost column is
empty:

```
step              pass   change  usd/correct  sec/correct  reasoning/correct
search + fetch    12/20          -            28.59        594
+ citation check  12/20  ~+0     -            30.76        634
+ plan slot       13/20  ~+1     -            35.81        398
+ sub-agents      0/20   ~-13    -            -            -
+ critic          0/20   ~+0     -            -            -
+ cost cap        0/20   ~+0     -            -            -
```

The last three rows didn't run. Gemini's free tier allows 500 requests a
day per model, and each question on the sub-agents step sends three
sub-agents. The quota ran out early in that step, and every call after it
got HTTP 429. So the table shows only the first three steps. On those,
Flash-Lite fails 8 of 20 with the bare step, and neither the citation check
nor the plan slot moves it by more than a case.

Claude Haiku 5.5, each case once, with `make exercise LIVE=haiku`:

```
step              pass   change  usd/correct  sec/correct
search + fetch    18/20          $0.0023      8.07
+ citation check  18/20  ~+0     $0.0024      9.18
+ plan slot       17/20  ~-1     $0.0037      14.27
+ sub-agents      19/20  ~+2     $0.0145      26.59
+ critic          19/20  ~+0     $0.0219      39.57
+ cost cap        17/20  ~-2     $0.0225      44.05
```

Haiku answers 18 of 20 with search and fetch alone. No step moves it by
more than two cases, and one run can't tell a change that small from noise.
Each step costs more: the sub-agents cost six times as much per correct
answer as the bare step, and the critic half as much again. Most of
Haiku's misses on the two hardest questions are turns that ran out of their
12 steps while paging through pages. The cost cap ended two turns that
would have answered.

On the stand-in model the ladder climbs 4, 8, 13, 16, 20, 20, one habit
fixed by each step. `TestEachStepFixesAHabit` checks it, and `scripted.go`
lists the habits.

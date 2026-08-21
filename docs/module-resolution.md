# Module Resolution

Reference for how `start` turns a user-supplied identifier into a module to act
on. This procedure is uniform across agents, roles, contexts, tasks, and skills.
The guiding principle is simple: search the installed config and the registry
index for matches, apply one match rule, and act on the result.

## Where it applies

| Surface | Identifier source | Category |
| ------- | ----------------- | -------- |
| `start task <id>` | positional argument | tasks |
| `--role <id>` (`-r`) | flag value | roles |
| `--context <id>` (`-c`) | flag value, repeatable | contexts |
| `--agent <id>` (`-a`) | flag value | agents |
| `start get <id>`, `start describe <id>` | positional argument | all library categories |
| `start uninstall <id>` | positional argument, repeatable | all library categories |
| `start config remove <id>` | positional argument, repeatable | all library categories |

The last two are installed-only surfaces; they reuse the engine's match rule
over installed config alone and differ from the rest as set out under
Installed-only surfaces below.

`start config get` and `start config edit` also apply the same literal name-only
match rule over installed config, but return the full match set rather than
reducing to one, so they are documented separately under Config inspection
surfaces below rather than listed here.

Model resolution (`--model`) is out of scope. A model is resolved against the
selected agent's `models` map, not against config and registry modules.

## The match rule

Resolution checks for an exact whole-name match first — for every non-path input
form, bare or category-qualified — then falls back to a query. The fallback mode
depends on the input form: a bare term falls back to a substring query, a
category-qualified term to a prefix query (see Input forms and Category prefix).

### Exact match

A single exact match is unambiguous by definition: the identifier equals the full name
of exactly one module, compared case-insensitively (any casing of a module's
complete name resolves directly). It resolves to that module directly — even when
the name is also a substring of longer names, and even without a TTY. An exact
match that exists only in the registry is installed first, then used, except on
`get`/`describe` of a skill, which fetch and emit without writing dests or
inventory.

On cross-category surfaces (`get`, `describe`) the same name can in
principle name one module in two categories — two exact matches. The exact tier
here spans both sources across every scoped category, so a same-name exact in
another category is detected whether it is installed or registry-only. A single
exact match is the canonical result and resolves directly (a registry-only prompt
module installs first; a registry-only skill is fetched, not installed). When the name is an exact match in more than one category —
two installed, an installed one alongside a registry-only one in another category,
or two registry-only ones when nothing is installed — it is a genuine ambiguity
that falls to the menu below, resolved by category-qualifying the name. This
behaves the same way with or without a TTY: a terminal shows the menu (installing
a chosen registry-only entry), a pipe returns the ambiguity error. Within a single
category names are unique — the naming standard's lowercase kebab-case keeps them
unique case-insensitively — so an exact match is always one module.

Note: because the cross-category exact tier consults the registry, a bare name on
`get`/`describe` depends on registry contents as well as installed config. A
script that resolves a bare name today, as the sole exact match, can begin
returning an ambiguity error later if a same-name module is published in another
category. Category-qualify the name (`category:name`) in scripts and pipes to pin
resolution to one category and stay immune to new registry twins. The
category-specific surfaces (`--role`, `--agent`, `start task`) are not affected:
their exact tier is scoped to one category, so no twin can arise.

### Fallback query

When there is no exact match, the identifier becomes a fallback query — a
substring query for a bare term, a prefix query for a category-qualified term
(see Category prefix) — that reduces to a set of matches and one decision:

| Match count | Behaviour |
| ----------- | --------- |
| 0 | Error: not found — or, when the registry index was unreachable, the retry-able error described under Search sources. |
| 1 | Use it. If the match is registry-only, install it first, then use it. |
| more than 1 | On a TTY, present a selection menu. Without a TTY, error and list the matches. |

The fallback query must be at least three characters; a shorter query is rejected
with an error before the fallback search runs. The minimum counts the name being
matched, excluding any `category:` prefix: both `ab` and `tasks:ab` are rejected,
because the prefix only narrows the category and adds nothing to the name match.
This floor is uniform across every surface and applies to both the substring and
prefix fallback modes. The exact-whole-name tier runs first and is exempt: a
complete canonical name resolves at any length, including names shorter than three
characters. The discriminator is exact-versus-partial, not length: `tasks:ci`
still resolves because `ci` is a complete name matched by the exact tier, while
the equally short `tasks:ab` is rejected because `ab` is not a complete name and
falls to a broad prefix scan.

The defining property is that resolution never silently runs one module when the
identifier is a *partial* match for several. The menu is reserved for partial
input; an exact whole-name match is not partial input and is never subject to it,
so within its category a module's complete canonical name always reaches that
module, in scripts and pipes as well as interactively (a registry-only name
additionally needs a reachable index; see Search sources). The one exception is a
cross-category surface (`get`, `describe`) where the same name is an exact match
in two *installed* categories: the bare name is then ambiguous and must be
category-qualified (`tasks:<name>`) to resolve without a menu. This covers a
registry-only same-name exact in another category too: the exact tier consults
the registry, detects the twin, and treats it as the same kind of ambiguity —
the same in a pipe (ambiguity error) as on a terminal (menu).

The naming standard forbids any name from being an ancestor of another within a
category, and the registry index is validated to enforce this (see the naming
standard's Leaf-Only Names section: `start get start/library/naming`). An exact
name can still appear as a substring of longer, unrelated names — as a prefix, an
interior segment, or a suffix, and whether or not they share a parent path.
Examples: `jira/item/read` inside `jira/item/read-only`, or `review` inside
`gitlab/pipeline/review`. The exact-match tier resolves the typed name directly
in every such case.

## Search sources

Matches are collected from two sources with no priority between them:

1. Installed config (the merged global and local configuration).
2. The registry index.

Results are merged and de-duplicated by `category:name`. A module present in
both sources is one match; the installed entry is used and no install occurs, so
"no priority" governs only which matches are collected, not this de-duplication.
A match that exists only in the registry is installed on selection (single match:
install then use; menu: install the chosen entry).

The enumeration of these two sources is a single shared primitive
(`modules.GatherCandidates`) used by every module-selecting surface except
`start update`. It returns
the full candidate set tagged by source and config scope with each entry's
index metadata retained, un-deduplicated and unfiltered; each surface layers its
own matcher on top. Resolution, removal, and the config-inspection surfaces use
the literal name-only matcher (`MatchByName`); `start search` and `start install`
use a regex/tag matcher (`MatchSearch`) over the same candidates, matching names,
descriptions, and tags. `start update` is the one surface not on the primitive:
it keeps its own installed inventory (`collectInstalledModules`, which carries
version and config-file metadata the primitive does not) and matches a bare
query by shared name or category substring. A `skills:` address uses the same
exact→prefix reduction as uninstall over that inventory, then dest-leaf
fallback on a miss. The installed-over-registry merge and
`category:name` de-duplication
described below are a resolution-only step layered after gathering, not part of
the primitive. What is unified is the gathering beneath the surfaces, not the
match rule on top.

The registry index is fetched lazily and its absence is non-fatal: when the index
cannot be reached, resolution proceeds against installed config alone. The
guarantees above about registry-only modules — exact-match install-then-use and
registry entries in the menu — are therefore conditional on a reachable index.
Installed modules resolve unconditionally. An uninstalled identifier is a
different matter: with the index reachable and the name found in neither source it
is genuinely not found (a not-found error), but with the index unreachable its
absence cannot be confirmed — the name may well exist in the registry — so the
failure is reported as a transient, retry-able error rather than not-found. This
matches how the rest of `start` treats an unreachable registry (`install`,
`update`, and `search` with no local match all report the same transient error).
Scripts and pipes that must resolve a registry module should install it ahead of
time.

## Installed-only surfaces

`start uninstall` and `start config remove` resolve through the same engine, but
their candidates come from installed config alone. They share the exact-tier-
then-fallback reduction and the cross-category-versus-`category:name` distinction
described above, and differ from the registry-backed surfaces in five ways:

- Installed-only: candidates are the selected scope's installed modules. The
  registry index is never fetched, and a registry-only name is never auto-
  installed — it is simply not found.
- Scope-bound: resolution and removal act within exactly one scope — global by
  default, local under `--local`. A module present only in the other scope is not
  found. (This differs from the registry-backed surfaces, which match against the
  merged global-and-local config.)
- No floor: the three-character fallback floor is dropped (floor 0), so one- and
  two-character substring or prefix queries resolve. The floor exists only to
  bound the registry-wide search on `install`; removal searches a small installed
  set where it is unnecessary.
- No locator: a filesystem path or `http(s)://` URL is rejected — it cannot name
  an installed module.
- Otherwise identical: a bare query is a cross-category substring search, a
  `category:name` query a category-scoped prefix search, and the exact-whole-name
  tier runs ahead of both, exactly as for the registry-backed surfaces.

Code: `internal/cli/removal.go` (`installedMatcher`, `matchInstalled`,
`removalResolveScope`, `removalScope`) over the shared `selector.match` /
`matchSource` seam in `internal/cli/engine.go`, with the floor passed as 0.

## Config inspection surfaces

`start config get` and `start config edit` select an installed entry to display
or edit. They gather candidates through the same shared primitive and apply the
engine's literal, case-insensitive, name-only rule — the exact-whole-name tier
first, then a substring fallback — across all four categories of installed config
in the chosen scope. (`config get` reads the merged config by default, or the
single scope under `--global` / `--local`; `config edit` reads merged or, under
`--local`, local.) They are not regex matchers: matching is the same literal
exact-then-substring rule every other name-only surface uses. They differ from
the resolution surfaces in two ways:

- Full match set, not one: a query keeps every literal-name match rather than
  reducing to a single module. `config get --json` emits the set as an array; a
  genuine multi-match menus on a TTY (and errors on a pipe), and `config edit`
  selects one from the same set. The exact tier still short-circuits the
  substring fallback, so a bare name that is also a prefix of installed siblings
  (`claude` alongside `claude/edit`) resolves to just that name; the siblings
  stay reachable via a non-exact substring query.
- No registry, no floor, bare names only: like the installed-only removal
  surfaces they never fetch the registry and impose no three-character floor;
  the query is a bare cross-category name (no `category:` prefix).

## Input forms

The identifier is interpreted before matching. The exact-whole-name tier runs
first for every non-path form; the Fallback mode column applies only when no
exact match exists:

| Input | Interpretation | Fallback mode |
| ----- | -------------- | ------------- |
| `foo` | bare term | substring over the name |
| `foo/bar` | bare term containing a slash | substring over the name |
| `tasks:foo` | category-qualified term | prefix over the name, scoped to that category |
| `/foo`, `./foo`, `~`, `~/foo` | filesystem path | no search; read the file directly |
| `http://…`, `https://…` | remote locator | no search; fetch the content directly |

Notes:

- The exact tier precedes the fallback for qualified input as well as bare: a
  category-qualified complete canonical name (`tasks:jira/item/read`) resolves
  to that module directly, even when it is a string-prefix of a sibling
  (`jira/item/read-only`) that the prefix fallback would otherwise also match.
- Matching is case-insensitive and targets the module name only. Description and
  tag matching belong to `start search`, not to resolution.
- A bare term is a substring match: `foo/bar` matches `foofoo/barbar` because the
  literal `foo/bar` appears inside it. A slash in a bare term is an ordinary
  character, not a path separator.
- A leading `/`, `./`, or `~` (including a bare `~`), or an `http://` or `https://`
  scheme, marks a locator. The locator is read directly — a path from the
  filesystem, a URL by fetch — and the search procedure is skipped entirely. This
  applies to every surface that yields a document body: the `--role` and `--context`
  flags, `start task`, and the cross-category `get`/`describe`, which read and
  display the content directly. (Mechanically, `start task` intercepts the
  locator itself before invoking the engine rather than using the engine's
  locator bypass; the observable behaviour is identical.) The sole exception is
  `--agent`: it accepts neither
  a filesystem path nor a URL, because an agent is a structured configuration rather
  than a document body, so a locator supplied to `--agent` is an error.
- A remote fetch is bounded: it follows the response under a timeout and a size
  cap, and refuses a `text/html` body so a rendered file page or a soft-404 is not
  injected verbatim. Only locators typed at the CLI are fetched; a `file:` field
  declared inside a module or config is always read from the local filesystem and
  is never fetched over the network.

## Category prefix

A `category:name` identifier names one of the library categories — `agents`,
`roles`, `contexts`, `tasks`, `skills` — and navigates that category's namespace
from its root. Names
within a category are paths (`jira/item/review`), so the qualifier scopes the
search to the named category and descends from `name`: the fallback matches names
that begin with the supplied term (a prefix), where a bare term matches the term
anywhere in the name (a substring). The exact-whole-name tier still runs first, so
a qualified complete name resolves directly; prefix matching applies only when no
exact match exists.

The two fallback modes are the two ways you reach for a module. A bare term is a
fuzzy "find it anywhere" search. A category-qualified term is precise namespace
navigation: you name the category and the start of the path. `start task review`
finds any task whose name contains `review`; `start task tasks:review` finds only
tasks whose name begins with `review`. The qualified form is therefore not a
scoped synonym for the bare form — it asks a different question, and on a
category-specific surface (where the category is already fixed) that different
question is the only reason to add the prefix.

Prefix rules by surface:

- Category-specific surfaces (`start task`, `--role`, `--context`, `--agent`):
  the prefix is optional. When present it must equal the surface's own category;
  a mismatched prefix is an error (for example `roles:foo` passed to `start task`).
- Cross-category surfaces (`get`, `describe`): no prefix searches
  all library categories; a prefix narrows to the named category.

A `skills:` query that misses as a prefix then tries the dest leaf: exact
inventory key first, then keys whose last path segment equals the name
(`skills:one-by-one` → `finding/one-by-one`). Installed keys are tried first;
if none match, `get`/`describe` try the registry index. Zero is not-found, one
is used, more than one menus or errors. `start uninstall` and `start update`
use the same exact→prefix reduction, then the same leaf rule over installed
inventory only — so `skills:finding` with two `finding/...` keys is a prefix
multi-match on all four surfaces.

Examples:

- `tasks:jira` matches `jira/item/review` and `jira/item/backlog/review` (both
  begin with `jira`), scoped to tasks.
- `tasks:review` matches only names beginning with `review`. It does not match
  `jira/item/review`, because that name does not begin with `review`; use the bare
  term `review` for the anywhere-in-the-name search.

## Selection

When more than one match is found and a TTY is present, list the matches and
prompt for a choice. Each entry is shown with its source (installed or registry).
On the category-specific surfaces (`start task`, `--role`, `--context`,
`--agent`) the entry is its bare name, since every match shares the surface's one
category; on the cross-category surfaces (`get`, `describe`) it is shown as
`category:name`, because the category is what distinguishes the matches. Accept
either the entry number or a typed name. A typed name that uniquely identifies one
shown entry selects it.

The menu shows at most 20 matches; a larger set is truncated with a
"Showing N of M matches" note advising a more specific query. Number and
typed-name selection operate over the shown entries only, so a match beyond the
cap must be reached by refining the query.

Without a TTY, return an error that lists the matches in the same form — bare
names on a category-specific surface, `category:name` on a cross-category one —
and instructs the user to specify an exact name, category-qualify it
(`category:name`) when the collision spans categories, or run interactively. The
listed forms are valid command arguments that round-trip back to the same entry.

## Per-category behaviour

- Tasks, roles, agents: resolve to at most one module via the match rule — a
  single result is acted on directly; several matches menu on a TTY or error
  without one.
- Contexts: each explicit `--context` term resolves independently via the match
  rule. Multiple terms select multiple contexts; within a single term the match
  rule still holds (one term that matches several contexts menus or errors). The
  `default` and `none` sentinels are not searched. Required and default contexts
  load automatically and are not subject to term resolution.
- Agents: the procedure runs only when `--agent` is supplied. Otherwise the
  configured default agent is used without resolution. An agent identifier is
  always a name; `--agent` does not accept a filesystem path.
- Sentinels skip resolution entirely: `--role none` skips role assignment, just
  as the context `none` and `default` sentinels above are never searched.

## Worked examples

Assume installed and indexed tasks include `cwd/project/review`,
`jira/item/review`, `jira/item/backlog/review`, and `gitlab/pipeline/review`.

| Command | Matches | Result |
| ------- | ------- | ------ |
| `start task review` | all four (substring `review`) | menu on TTY, ambiguity error otherwise |
| `start task jira/item/review` | exact match | run it |
| `start task pipeline` | one (substring) | run it |
| `start task tasks:jira` | the two `jira/...` tasks (prefix) | menu or error |
| `start task tasks:gitlab` | one (prefix) | run it |
| `start task ./my-task.md` | n/a | read the file, no search |
| `start task nonsense` | none | not-found error |
| `start task rv` | exact tier: no match | error: fallback query under three characters |

Exact match takes precedence over substring matches. With installed tasks
`jira/item/read` and `jira/item/read-only`:

| Command | Matches | Result |
| ------- | ------- | ------ |
| `start task jira/item/read` | exact `jira/item/read`; substring match `jira/item/read-only` | run `jira/item/read` directly, including without a TTY |
| `start task read` | substring: both | menu on TTY, ambiguity error otherwise |

The first command never menus: the identifier is the complete name of one module.
The naming standard forbids a name that is an ancestor of another, but an
exactly-typed name can still be a substring of longer, unrelated names — here
`jira/item/read` inside `jira/item/read-only`, and elsewhere across different
parent paths. The exact-match tier resolves the typed name regardless of how it
overlaps those longer names.

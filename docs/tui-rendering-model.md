# TUI rendering model

Date: 2026-09-15. Describes how `makai --tui` paints the terminal after the inline
renderer rewrite, what each layer owns, and the invariants tests and future changes
must keep. The previous scroll-region design (bottom-anchored frame plus DECSTBM
history insertion) is gone; this document replaces the implicit contract it had.

## Why it changed

The old inline path (`zigzag` `Program.render` with `inline_bottom_viewport`) anchored
the live frame at the bottom of the screen and, when the frame shrank, drew the new
frame at the *old* top row while clearing below it. The next paint anchored at the
bottom again, so a stale copy of the frame stayed on screen above the live one. The
app then inserted flushed history into a scroll region above the frame, which
scrolled the stale copy into scrollback. Closing the model picker was enough to
trigger it, and every turn left a stale composer/status pair in the terminal
history. Frame growth also overwrote visible history rows because the renderer never
scrolled the terminal before drawing a taller frame.

## Layers

### `zigzag` Program (vendored, `zig/vendor/zigzag/src/core/program.zig`)

`Options.inline_bottom_viewport = true` now means **cursor-relative inline live
region**, the same model Bubble Tea's standard renderer and Ink's `<Static>` use:

- The Program tracks `last_line_count`, the number of rows the live frame currently
  occupies. The cursor rests on the frame's last row after every paint.
- A paint moves to the top of the live region (`CUU last_line_count-1`, `CR`), writes
  any queued print-above text, then the frame, then `ED 0` (erase below) so a shorter
  frame leaves nothing behind. Rows are written with `\r\n`; when the cursor is on the
  bottom row the terminal scrolls naturally, which is how history reaches scrollback.
- The Program keeps the previous frame's rows. When nothing was printed above and the
  live region has not moved, a row identical to the one already on screen is skipped
  with a bare line feed instead of being rewritten (the last row is always rewritten,
  since `ED 0` precedes it). A spinner tick therefore costs one or two rows rather
  than a whole screen, and modal panels are emitted once, not on every animation
  frame — this is what keeps the PTY harness's "no second approval prompt" assertion
  meaningful.
- Frame lines are clamped to the terminal width (ANSI-aware) so an over-wide line can
  never wrap and desynchronise the row count. A frame taller than the terminal keeps
  its tail. `EL 0` is only emitted after lines narrower than the terminal: some
  terminals (xterm) erase the last column when `EL` runs from the pending-wrap
  position, which used to eat the right border of full-width panels.
- `Context.printAbove(text)` queues persistent lines. They are written on the next
  paint, above the frame, inside the same synchronized-output block, so a transcript
  entry that moves from the live frame into history never visibly moves.
- `Context.requestClearScreen()` clears the screen and scrollback (`ED 2`, `ED 3`)
  before the next paint and resets the live region. `Cmd.println` routes through
  `printAbove` in inline mode; a full-screen program (`inline_bottom_viewport = false`)
  keeps the immediate cursor-save/home/restore write, since its render path never
  drains the print-above queue.
- Quitting erases the live region with the same reflow-aware row estimate the resize
  relayout uses (`last_line_widths` against the current width), so a `/quit` that lands
  inside the 150 ms resize debounce still clears every row the terminal rewrapped the
  old frame into instead of counting on the pre-resize row count.
- Resize is debounced (150 ms after the last `SIGWINCH`; nothing is painted while a
  resize is pending, because terminals reflow the old rows and a cursor-relative
  repaint would land in the wrong place). When it fires the Program performs a
  relayout inside one synchronized-output block: it moves to the top of the old live
  region, erases it, writes any print-above text that was queued during the debounce
  window, scrolls the whole screen into scrollback with `height` line feeds, homes the
  cursor and resets the live region. Before that it dispatches `window_size` to the
  model, and the app answers by rewinding its flush cursor so the frame itself carries
  the tail of the transcript at the new width; the screen is repainted from the top as
  `[history tail][frame]` with the status line and working-directory row back on the
  bottom rows and no blank rows
  pushed into scrollback — at most one screenful of pre-resize output is duplicated in
  scrollback per resize gesture.
- The top of the old live region is estimated for reflowing terminals (iTerm2,
  Terminal.app, Kitty, VS Code, tmux rewrap hard lines when the window narrows): the
  Program remembers the ink width of every painted row (trailing padding excluded) and
  counts `ceil(width / new_width)` rows per line. Terminals differ on whether written
  trailing spaces rewrap, so the estimate deliberately ignores them: undercounting
  leaves an invisible blank row in scrollback, overcounting would erase real history.
- When the program exits in inline mode it erases the live region and drains queued
  print-above text, leaving only the transcript in the terminal followed by the shell
  prompt on a fresh line. The real cursor is hidden; the composer draws its own caret.

`Context.deinit()` frees the print-above buffer; anything that builds a `Context` by
hand (tests) must call it.

**The TUI owns the terminal.** The renderer tracks the cursor by counting the rows it
wrote; any other write to the terminal shifts the cursor and every later paint lands
in the wrong place (and, since paints rewrite only changed rows, stale rows of an
earlier frame stay on screen). One stray line printed from the end of a full-width
status row costs two rows: the wrap plus the newline. `tui_app.run` therefore
redirects fd 2 to `~/.oapx/tui-stderr.log` (append, mode 0600; `/dev/null` when the
home directory is unavailable) for the whole session and restores it on exit, so
`std.debug.print`/`std.log` output from libraries — the OAuth token exchange warns
this way — is recorded instead of drawn. Never write to stdout or stderr from TUI
code paths; add a transcript row instead.

### App (`zig/src/tui/app.zig`)

- `TuiModel.render_mode`: `.auto` (inline when a terminal is attached, full-transcript
  otherwise), `.inline_history` (forced, used by the e2e driver), `.full_transcript`.
- The transcript is treated as a **row stream**: each entry renders to a block of rows
  (a blank separator row for detached entries, none between consecutive tool rows,
  then the entry's rows). `App.inline_history_flushed` is the index of the first entry
  with unflushed rows and `App.inline_flushed_rows` is how many rows of that block are
  already in scrollback, so flushing is row-granular. Rows go to scrollback (via
  `printAbove`) only when the unflushed stream overflows the **flush budget**, the rows
  left on screen once the blank separator, the composer at its minimum one-row height
  and the status line are subtracted — the composer is budgeted like a modal, so
  growing it covers transcript rows instead of flushing them and shrinking it later
  leaves no blank rows behind —
  and never from active entries (assistant, thinking, tool summary, tool result, user
  echo). Quitting flushes everything, including active entries.
- The live frame is the **tail of the unflushed stream** that fits above the chrome,
  followed by a blank row, the modal panel (approval, picker, command palette) if any,
  the composer and the status line. The frame is therefore bottom-anchored once the
  transcript fills the screen: opening a modal covers the tail of the history instead
  of scrolling it into scrollback (the flush budget ignores modals, so covered rows
  are never flushed), and closing it repaints the same rows with the composer back on
  the bottom row. Because scrollback rows plus frame rows are always a prefix of the
  stream, the terminal never shows a duplicated or missing row. The frame is padded
  with blank rows at the top to exactly `height`, so it is bottom-anchored from the
  first paint rather than only once the transcript fills the screen: without the
  padding a short transcript left the frame the height of its own content and the
  status line sat wherever the shell cursor happened to be, with the rest of the
  viewport blank below it. The cost is that content growth shifts every row up, so a
  paint that adds a transcript row rewrites the whole frame instead of only the rows
  below the insertion; a paint that changes no heights (a spinner tick) still rewrites
  only what changed, because rows keep their index.
- Active entries render with `live = true` (spinner, caret, tail-clipped thinking, the
  "waiting for <model>" line when streaming with nothing active); inactive rows render
  identically whether painted in the frame or flushed above it.
- On `window_size` the app rewinds the flush cursor so the unflushed stream fills the
  flush budget at the new width; the Program's relayout then paints that window from
  the top of the cleared screen.
- `/clear` empties the transcript, resets the flush index and requests a screen
  clear; the "transcript cleared" row is the first thing printed afterwards.

### State (`zig/src/tui/state.zig`)

- `TranscriptKind.welcome` renders the start banner. `active_thinking_entry` tracks
  streamed reasoning; a thinking block that starts while the assistant placeholder is
  still empty is inserted **before** the placeholder so reasoning precedes the answer.
- `StatusState.streaming_since_ms` / `streaming_elapsed_ms` drive the elapsed timer in
  the status line (the app refreshes the elapsed value every tick).
- `ComposerState` gained word/line editing: `moveCursorWordPrev/Next`,
  `deleteWordBeforeCursor`, `deleteToLineStart/End`, `deleteAtCursor`. It also tracks
  `scroll_row` (first visible visual row of the draft, adjusted minimally in
  `renderChrome` so the cursor row stays inside the window) and `goal_column` (the
  sticky column for consecutive Up/Down moves); `clear` resets both.

## Visual language (`zig/src/tui/theme.zig`, `views/`)

- Entries: one-space gutter, role glyph and name in the role colour, dim `· HH:MM`
  timestamp, body indented three columns and capped at 108 columns. User text is a
  left-aligned soft block; assistant prose gets inline styling (bold, italic, code
  spans), bullets, numbered lists, headings, block quotes, and fenced code blocks with
  a language tag — all applied to rows *after* the #254 sanitizer and wrapper, one
  self-contained styled row at a time. `renderAssistantPlain` remains the unstyled
  wrap engine and its exact-output tests are unchanged.
- Tool calls: one row, `◆ Label  argument` on the left, status on the right
  (`⠋ running`, `◌ awaiting approval`, `✓ 342B · ~87 tok`, `✗ failed`,
  `■ interrupted`). Result rows render as dim `⎿` lines capped at eight rows. The row
  text still comes from `state.zig` (`◈ Label "arg" ok output=NB …`); the renderer
  parses it, so the persisted-tool-call model of #273 is unchanged.
- Live assistant entries show a spinner in the header and a caret after the last
  character. Thinking shows dim italic text, tail-clipped while live.
- System rows: a short single-line note renders as one muted `•` row. Anything longer
  or multi-line (the OAuth login prompt, restore summaries) renders under a `System`
  header and is **wrapped, never truncated** — over-long tokens are hard-broken at the
  column limit. URL tokens become OSC 8 hyperlinks: every visible fragment of a wrapped
  URL carries the full target and a shared `id=`, so terminals that support OSC 8
  (iTerm2, Kitty, WezTerm, Ghostty, VS Code, Windows Terminal, VTE) treat the pieces
  as one clickable link, and each fragment opens and closes its link inside its own
  row so the renderer's row-diff paints never leak link state into other rows.
- Composer: rounded panel whose border colour tracks state (idle grey, typing accent,
  streaming pulse, approval warning, login magenta). Typing `/` opens a command palette
  above it; `Tab` completes the first match. The raw draft is laid out into visual rows
  at the content width by `tui_text.layoutRows` (wide codepoints never split; a cursor
  past the last cell of a full row — or on the newline ending one — lands on the next
  row), and the panel grows with the
  draft up to `min(12, height/3)` content rows, floor 1. Beyond that the window follows
  the cursor and muted `▲ N` / `▼ N` markers in the top/bottom border count the hidden
  rows. Tab renders as `→`, other C0 bytes and DEL as caret notation (`^G`, `^?`), C1
  and invalid UTF-8 as `?`, so a pasted escape sequence can never reach the terminal
  raw; pastes normalise CRLF to LF. Masked login input stays on one windowed row.
- Status line: `provider/model`, context gauge with a usage percentage coloured by
  band (green below 60%, yellow 60–75, orange 75–85, red 85 and up), `queue`, a bare
  permission value (`ask`/`bypass`/`pending`), cost (once tokens are known), a bare
  thinking level (`off` included), `turns:`, the state (`idle` or spinner + elapsed),
  and the token rate, plus a right-aligned key hint. When the row overflows, the context
  segment first shrinks to just the coloured percentage, then segments drop whole by
  priority (the rate, turns, thinking, cost, the hint, `ask` permission, queue, drops,
  model, backpressure, context) behind one trailing `…`; the state segment — and
  `bypass` or `pending` — are never dropped, and the row is clipped with `…` if even
  they do not fit. The rate is first to go because it is the most transient figure in
  the row; the second row below carries the path and never competes with it, so nothing
  in the rate's drop order can be said to drop before the path does.
- The rate is a `~`-marked estimate or an unmarked measurement, and the mark always
  means the same thing: **a mark means an estimate, an unmarked figure is measured.**
  It is the live figure while a message streams — necessarily an estimate, because
  usage only arrives at `message_end`. While a run is in progress that is the figure
  the run has just produced, since the previous turn's would describe a run already
  finished; once the run ends the row shows the average since the last model switch,
  because an idle line is asking how fast this model is, not what one reply happened to
  manage. A run that has measured nothing yet falls back to the average rather than
  showing nothing, so the segment never appears and disappears at the start of a run.
  The average never mixes the two kinds: it is the mean
  of the measured turns alone, and only when the model has produced no measured turn at
  all does it fall back to the mean of the estimates, marked. So a provider that
  reports no usage leaves the average measuring nothing rather than reading as slow, and
  a provider that reports usage is never diluted by a guessed sample.
- The rate's denominator is **the time the stream was actually producing**, which is not
  the status bar's elapsed: that clock starts at `turn_start` and includes tool calls.
  The rate runs a second clock from an assistant message's first delta to its
  `message_end`, summed over the turn's assistant messages, so time spent in tools never
  counts as slow generation. A tool call is production, so `tool_call_delta` starts the
  clock as well: a reply that is only a tool call is measured over the span it was
  generated in, not dropped. A message that arrives whole with no stream at all — the
  non-streaming result fallback — contributes neither tokens nor time, because there is
  no span to divide by, and counting its tokens with no time would inflate the figure
  several-fold while showing it unmarked. A turn is marked `~` when *any* of its messages
  was estimated, and its measured and estimated parts are pooled separately, so a
  multi-message turn cannot present a mixed total as measured. The runtime always pushes
  `agent_end` immediately after the final `turn_end`, so a turn end that finds its
  accumulators already empty does not clear the standing figure: the previous turn keeps
  its number, and a run of two or more turns still shows the last turn that actually
  produced tokens rather than falling through to the average. Bytes convert at the
  agent's own divisor, `(bytes + 3) / 4`.
  The averages reset when the model in effect changes, which is what `/model` and
  `/provider` do.
- The cost segment beside it is **computed, not reported**: it is the model's
  `cost.input` multiplied by the prompt estimate, so the row now carries one figure from
  what the provider reported (the rate) beside one this repository worked out (the cost).
- A second row under it shows the working directory on the left, muted, collapsed
  to `~` under the home directory and left-truncated with `…`, and the git branch at
  the right end; the row hides on terminals shorter than 12 rows. The branch is read
  from the repository rather than from a `git` process: the working directory and each
  of its ancestors are probed for `.git/HEAD` until one is found, nearest first, so a
  subdirectory of a repository resolves the way `git` resolves it. A `.git` that is a
  `gitdir:` pointer file — a linked worktree or a submodule — is followed, with a
  relative target resolved against the directory holding the pointer, and that
  directory's `HEAD` is read instead. A `ref: refs/heads/` line gives the branch name
  and any other line gives the first seven characters of the commit id for a detached
  HEAD. The nearest `.git` decides the row and nothing above it is consulted, so each of
  these leaves the right end empty rather than falling through to an enclosing
  repository — which is what `git` does when it errors on the repository it finds: no
  repository above the working directory, a `.git` it cannot inspect, a `.git/HEAD` it
  cannot read, and a `.git` pointer file that does not parse. The read happens when the
  working directory changes and on a slow tick — every 100th tick, five seconds — so a
  `git checkout` shows up without a `git` process per render. When the row is too narrow
  for both, the branch is dropped and the path takes the full width.

  The path is the directory the agent is working in, not the one the TUI started in.
  `workspace_root` is a required, model-supplied argument on every workspace tool, and
  the agent loop forwards the model's arguments unchanged, so the last absolute
  `workspace_root` a tool call names is the directory that call is for. The TUI reads it
  off `tool_execution_start`, whose `args_json` the runtime already carries. That event is
  pushed before the permission engine evaluates the call, so the row leads the call
  rather than following it, and it moves to the directory a refused call named even
  though the call then never ran there — the row is where the agent is working, which is
  what the model's next call will build on, not a receipt for what already happened. A
  call with no `workspace_root`, or a relative one, leaves the last known value alone,
  and the initial value is the directory the TUI started in. `/clear` and a session
  resume return the row to the session root; resume suppresses the follow while it
  replays the session's persisted events, so a directory from before the resume does not
  come back with them.

  A path outside the session root is shown, not hidden, and rendered bold in the warning
  colour instead of muted — leaving the session's workspace is worth seeing. Both paths
  are resolved with `std.fs.path.resolve` before that comparison, so a `workspace_root`
  that climbs out with `..` is judged on where it lands rather than on how it is
  spelled; the row still shows the path as the tool call wrote it. This is a label and
  nothing more: it is never read by `PermissionEngine`, whose `workspace_root` is fixed
  when the app initialises and continues to be what `isInsideWorkspace` checks against,
  so the row cannot widen what a tool call is allowed to reach. Note the two are
  genuinely different, since the engine's boundary test covers only `.read` and `.write`
  and a relative path is joined against the model-supplied root — tracked in #587.

  There is no directory the agent changes itself: each call names its own root, so a `cd`
  does not persist and nothing needs reporting a resulting directory. #586 carries that
  and the design question behind it.

## Credentials and the model catalog

- Credentials stay in the macOS keychain (item label "makai credentials", service
  `ai.hyperneo.oap`, account `auth.shared.json`; the pre-existing `auth.json` item is
  migrated on first read). Keychain access lists are bound to the accessing app's
  signing identity: a Developer ID signed release is identified by its team ID, so
  "Always Allow" persists across updates, whereas an unsigned local build is identified
  by its code hash and every new build is a new application to securityd (a
  `partition_id` entry it adds for non-Apple-signed apps enforces this even when the
  ACL lists no applications). Expect one login-password prompt per new dev binary;
  identical rebuilds do not re-prompt. `OAPX_KEYCHAIN_SERVICE` overrides the service
  name so tests can use an isolated item; the 0600 `~/.oapx/auth.json` file remains
  the fallback only when the keychain is unavailable.
- `/login` shows each provider's state: `✓ logged in` (OAuth), `✓ api key` (stored key),
  `✓ env key` (key in the environment), or `expired · login again`. The state is read
  when the picker opens and after a login completes; when stored auth cannot be loaded
  (no `HOME`, unreadable or malformed store) the environment scan still runs, so an
  `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_API_KEY` or `KIMI_API_KEY` user sees `✓ env key`
  rather than nothing.
- The model catalog lists Kimi models whenever a credential exists (a stored `/login
  kimi` or `KIMI_API_KEY`, the stored one first): it fetches `GET /v1/models` on the
  region's own host — `api.kimi.com/coding` or `api.moonshot.ai`, whichever the stored
  login or `KIMI_REGION` names — with that key, caches the body under
  `~/.oapx/model_catalog/kimi.json` (`kimi-global.json` for the global region) on the
  same 24-hour window and stale-copy fallback as Anthropic's, and falls back to the
  static `kimi-k2.7-code` when both fetch and cache are unusable. Each entry takes its
  display name, context window, reasoning flag and text/image input from the response's
  `display_name`, `context_length`, `supports_reasoning` and `supports_image_in`; the
  endpoint reports no output cap, so every entry keeps the 16 384 default. A selected
  Kimi model resolves its key the same way at request time.
- The model catalog lists Anthropic models whenever Anthropic credentials exist
  (OAuth in storage or `ANTHROPIC_API_KEY`): it fetches `/v1/models` with the stored
  token, caches the response under `~/.oapx/model_catalog/anthropic.json`, and falls
  back to a static Claude list when the fetch fails. A cached response is reused at
  startup for 24 hours; after that startup fetches again and only falls back to the
  stale copy when the fetch fails, and `/model` always fetches. Prices come from a
  small table keyed by model prefix; for an unknown model the status bar hides the cost
  instead of guessing a rate. Dated aliases of the default model are folded into it,
  and the fold keeps the catalog entry's limits (max output tokens, context window,
  reasoning flag, and price when known) on the default entry, so the built-in fallback's
  conservative `max_tokens` only applies when no catalog entry matched.
- Tool rows take their status (`running`, `✓`, `✗ failed`, `■ interrupted`) from the
  linked `ToolEntry`, never from words in the row text; the status word written into
  the row is parsed only for rows without a live link (a resumed session's transcript),
  and that parse skips the known label so a tool named "Deployment failed checks" is
  not read as failed.
- A tool row's argument is fitted to the terminal width by the row, not clipped when
  the summary is written. A shell tool's row puts the call's `command` under the
  title, lexed by `tui/shell_highlight.zig` (command words, flags, strings, variables,
  operators, heredoc bodies, comments), wrapped to the body width and capped at 12
  rows with a `… +n more lines` marker. When the title's argument is that command,
  the title drops it so the command shows once.

## Keys

`Enter` send (steer while streaming), `Tab` while streaming queue the draft as a
follow-up that is sent when the turn stops (it waits above the composer until then,
and the inline window reserves its rows so no transcript row hides behind it; a
draft starting with `/` is never queued),
`Shift+Enter` newline, `Esc` clear draft →
abort turn → close modal, `Ctrl+C` abort/clear first and quit on a second press
within ~1.5 s (immediate quit when idle with an empty composer), `Ctrl+D` quit on an
empty idle composer, `Tab` complete the slash command the palette selects,
`Ctrl+Y` copy the last reply, `Shift+Tab` cycle thinking, `Up/Down` move the slash
palette's selection while it is open; otherwise they move the cursor one visual row
inside the draft (keeping the goal column across consecutive presses, snapping to the
start of a wide codepoint) and, at the first/last row, walk history — once a recalled
entry is showing unedited they keep walking history, and pressing Up on the first row
of an edited recall discards the edits and walks on, `PgUp/PgDn` (and the mouse wheel
when mouse reporting is on) scroll the live window over the whole transcript row stream,
`Ctrl+A/E` line home/end, `Ctrl+U/K` cut to line start/end, `Ctrl+W` / `Alt+Backspace`
delete word, `Ctrl+Left/Right`, `Alt+Left/Right`, `Alt+B/F` word moves, `Delete`.
`Enter` on an open palette runs its selected command.

In the model, login and permission pickers, typing filters the list: every
space-separated term must appear, case-insensitively, in an item's label or detail.
`Backspace` edits the filter, `Up/Down` and `PgUp/PgDn` move, `Enter` selects and `Esc`
closes; reopening a picker clears its filter.

## Context window

The window in effect is the model's own `context_window` until the session says
otherwise. `/context` with no argument reports it, naming the model, the provider and
the ceiling; `/context <tokens>` sets it for the session, and `/context default`
restores the catalog's window. `oapx --tui --context-window <tokens>` sets the same
window at startup, and a later `/context` in that session replaces it. A count is a
whole number, optionally scaled by `k` or `m`, so `1m`, `272k` and `1000000` are the
same request; anything else — a sign, a decimal, an empty string, a count that
overflows — is refused rather than rounded.

The window in effect is what the compaction budget is computed against and what the
context gauge divides by, so the two cannot disagree: the runtime hands the agent a
model carrying the session's window everywhere it hands it a model, which is at start,
at a model switch, at a picker selection and when the catalog is refreshed.

A value is refused when it is above the ceiling the model's row records in the provider
catalog, and the refusal names the model, the ceiling and the window still in effect.
Lowering is never refused. A model that records no ceiling is accepted at any size,
because the window such a model arrives with is this repository's generic default rather
than a statement about the model — the reply says so, and with it that the provider may
refuse a request that size. A refusal is a limit on what this repository will ask for,
not a claim about what the provider accepts: a window above what a model reports still
fails the turn with the provider's own overflow error, and `/compact` is the way out.

The ceiling follows the model, so a window the model in effect cannot take is dropped
rather than carried: `--context-window` above the first model's ceiling is dropped before
the first turn, and so is a session's window when a model switch lands on a model whose
ceiling is lower. Each drop is a System entry naming the window, the model and what it
takes, and the model's own window is in effect from then on. Setting a window below what
the model reports is never dropped.

## Automatic compaction

`/autocompact <percent>` sets the share of the context window at which the session compacts
itself, `/autocompact off` turns that off, and `/autocompact` with no argument reports the
share in effect. `off` is what a session starts with, so nothing compacts on its own until
it is asked to. A percent sign is optional (`80` and `80%` are the same request) and the
share must be between 1 and 100: `0`, `101`, a sign, a decimal and anything that is not
digits are refused. `/status` carries the share on its own line, so the value is visible
without a second command.

The share measures the same figure the context gauge shows — the prompt estimate the
provider was last sent — plus the message about to be sent, against the window in effect.
It fires before a turn is submitted rather than during one, because a turn that has already
overflowed is the failure this avoids. A message typed while a turn is streaming or a
compaction is running takes the normal path, and the next submit is the one that measures
again.

When it fires, the transcript says so, the compaction runs exactly as `/compact` runs it
(down to the transcript it writes), and the held message is sent when the compaction ends —
including when it was cancelled or failed, because a message the user typed is not something
to drop quietly; that case says the history is unchanged. A message the queue resumes while
the compaction finishes is steered rather than submitted, so waiting for a turn never blocks
the tick thread. A `/resume` that lands before the compaction ends drops the held message
with a note saying so, at the top of the resume, so it cannot be sent into the session that
replaced it. A session whose history is already a summary, or empty, is not compacted
again, and one automatic compaction runs at a time.

## Recovery after a provider error

The HTTP retry policy is five statuses — 429, 500, 502, 503, 504 — plus a
transport failure, three attempts each with exponential backoff, and the
capability model's `max_retry_delay_ms` bounds the sleep rather than choosing
which errors retry. A 400 is outside that set, so one ends the run immediately
and the transcript shows the error and nothing else.

When a run ends that way the TUI waits about three seconds and then sends one
`continue` on the user's behalf, with a system line saying it is doing so and
the user message it sent visible in the transcript like any other turn. It does
this once per failure streak: if the automatic continue fails too, that is left
to the user, and a clean run or a turn the user sends themselves starts a fresh
streak. It never does it after an abort, after a 401 or 403 (the credential has
to be fixed, not replayed), or when the error is a context overflow that
`/compact` handles. Anything the user does inside the delay — submitting,
steering, queueing a follow-up, `Esc` or `Ctrl+C` — drops the pending continue,
`Esc` here meaning any of them, whether it clears the draft, aborts the run or
closes a picker. It waits rather than expires while a run is streaming or a
picker or approval is open, so the three seconds is a wait rather than a
deadline, and dropping it says so in the transcript rather than leaving the
earlier announcement standing. The continue goes out through the same path a
typed one does, so an `/autocompact` session compacts first and holds the
continue until the compaction ends. A follow-up
already queued when the run fails suppresses it entirely, because an
error-ended run does not resume the queue on its own, so the continue would be
a promise nothing keeps. Replaying a saved session is not a fresh failure: a
session whose last run ended in an error does not nudge on resume, because the
failure belongs to the process that hit it.

## Compaction

`/compact [focus]` replaces the agent's history with a summary the current model
writes. The request reuses the turn's system prompt, tools and thinking level so the
provider's prompt cache still applies, and ends with a user message asking for a
sectioned summary inside `<summary>` tags without tool calls; the optional focus is
appended to it. The history becomes that summary as a user turn plus a fixed assistant
acknowledgement, so queued messages still run through the normal continue path.
Before the request, the messages being replaced are written to
`~/.oapx/sessions/<session>/compaction-<n>.jsonl`, one message per line, and the
summary lists every transcript written so far in the session. A later transcript
starts with the summary before it, so the chain reaches the first message. When the
history does not fit in one request, the oldest turns are left out and the summary
says so. A provider error that reports an overflow retries with a quarter less
history, up to three attempts.

While compacting, the status bar reads `compacting` and `Enter` and `Tab` queue the
draft. `Esc` cancels the compaction and leaves the history unchanged, but keeps the
queued drafts: they are sent once the compaction ends, whether it completed, failed or
was cancelled. The result is a System entry with the message count, estimated tokens
before and after, the transcript path and the summary. The session file records the
result as a `compaction_end` event; a resume replays it by resetting the history to
the summary. None of this crosses the protocol: the agent loop runs in-process, and
the summary request is an ordinary model call.

A session is `~/.oapx/sessions/<session>.jsonl`, the conversation records a resume
replays. Streamed chunks (text, thinking and tool-call deltas, raw provider events,
tool progress) go to `<session>.stream.jsonl`, which a resume does not read; a reply's
thinking is also written to the conversation when the reply ends, folded into records
of up to about 700 KB. The model and provider are written with the first record and
again when they change, and `<session>.meta.json` holds them with the creation and
last-active times and the offset of the last completed compaction, so a resume starts
there and reads up to 256 KB before it for the screen; when no completed compaction
loads from there, as after a torn write, it reads the whole file. `/resume` lists
sessions from these index files as `title · local date and time · model`. A session's
title is the first line of its first message until its first reply ends; the current
model is then asked, once and in the background, for a title of at most six words,
which replaces it. Sessions from before the index take their first message from the
head of the file. Files written before this layout still load, skipping their provider
events, tool-call deltas and tool progress unparsed.

Scrolling: while `transcript_scroll` is non-zero the inline body is a window over the
full transcript rendered at the current width (rows already flushed into terminal
scrollback are re-rendered inside the window while it is scrolled), topped by a
`↑ SCROLL n% · PgDn to return` row. The offset is clamped to the rows above the tail
and written back, so paging past the top and then back down returns in one step;
submitting anything, resizing, or `/clear` snaps the window back to the tail. Key
updates do not reset the offset, so paging is not undone by the flush that follows
every update.

## Testing

- Unit tests build a real `zz.Context` (`TestContext` in `app.zig`) instead of passing
  `undefined`; the e2e driver runs in `.inline_history` mode and concatenates drained
  print-above text with the live view, so it exercises the production path.
- `scripts/tui-pty-driver.py` (Linux CI) drives the real binary. Because the renderer
  only rewrites rows that changed, the byte stream no longer carries a copy of the
  screen on every paint, so the harness feeds every chunk into a small VT screen model
  (`VtScreen`: cursor movement, erase, scroll regions, wrap, scrollback) and makes
  row-level assertions against `screen_text()` / `visible_text()` — "exactly one
  finalized tool row", "the steer echo sits directly above the abort row", "no approval
  panel on screen after `a`". Stream-based `wait_for` remains for *new* output and
  takes `since=` when two markers arrive in the same paint. Every recorded frame also
  stores the visible rows. The fixture provider accepts `<think>…</think>` at the start
  of a `text:` step to emit reasoning deltas.

import sys
sys.path.insert(0, "/private/tmp/claude-501/-Users-mike-Documents-GitHub-ratchet/f5519e05-1f50-4803-9a55-6fbc3753dac6/scratchpad")
from deck_common import *

DECK = 3
LABEL = "RATCHET · PART 3/5 · FORMAT, THINK, TURN"
prs = new_deck()
TOTAL = 21

title_slide(prs, DECK, "Format, Think, Turn",
    "The wire-level mechanics of talking to the fleet over Ollama",
    "Part 3 of 5. Almost everything in the first two pieces traces back to decisions made "
    "at the level of a single call to a model — and almost none of it looks like calling "
    "Claude's API.",
    title_size=46, series_total=5)

# 2
content_slide(prs, 2, TOTAL, LABEL, "The Call Underneath the Call", "Chat and ChatWithTools",
    bullets=[
        "Every verb reduces to one of two functions in a small Go client: Chat (a single "
        "request-response exchange) or ChatWithTools (a bounded loop of the same exchange "
        "with tools attached). Both talk to Ollama's HTTP API, not Anthropic's.",
        "Claude's API has spent years absorbing the problems in this deck into solved, "
        "invisible infrastructure. Ollama's hasn't — nobody built it to run a fleet of open "
        "models through a multi-turn structured-decision pipeline at production reliability.",
        "Ratchet built that layer itself, incident by incident.",
    ], bullet_size=17)

# 3
content_slide(prs, 3, TOTAL, LABEL, "What a Turn Is", "One exchange inside a bounded loop",
    bullets=[
        "The accumulated conversation goes out; the model's response — text, a tool call, "
        "or both — comes back; if a tool was called, its result gets appended and the loop "
        "goes around again.",
        "CRITIQUE runs up to six turns. WRITE's budget scales with how many functions the "
        "bead still needs. ADJUDICATE's tool turns end the moment its structured answer arrives.",
        ("Every turn has its own token ceiling, separate from the whole-call timeout. ", "A "
         "reasoning stream that doesn't know when to stop can wedge a generation slot for an "
         "hour producing nothing usable."),
    ], bullet_size=17)

# 4 stat
stat_slide(prs, 4, TOTAL, LABEL, "The Per-Turn Cap", "8,192",
    "generated tokens, per tool-loop turn — toolLoopNumPredict",
    "One observed case climbed to 14,500 tokens of pure thinking and was still climbing "
    "before this cap existed. At the cap, Ollama just ends the turn cleanly (done_reason: "
    "\"length\"), so the loop can detect the dead turn and recover instead of hanging silently "
    "for the full 60-minute client timeout. Claude Code's own tool loop rarely needs a "
    "defense like this — a frontier model reliably knows when it's done thinking.")

# 5 NEW — streaming vs transactional / why only EXECUTE gets a monitor
content_slide(prs, 5, TOTAL, LABEL, "Streaming vs. Transactional", "And why only EXECUTE gets a monitor",
    bullets=[
        ("One-shot decision verbs are non-streaming. ", "Tried streaming everywhere first, "
         "then deliberately reverted here: per-chunk overhead compounded on a verb "
         "generating thousands of tokens (RECONCILE) into a 1-minute call becoming a "
         "30-minute timeout. Batching the whole response is simply faster."),
        ("Tool-loop verbs stay streaming. ", "There the point isn't speed — it's watching a "
         "long generation happen token by token in the trace file, which is the only way a "
         "stalled or spiraling turn is diagnosable at all."),
        ("That same logic is why EXECUTE_BEAD, alone, gets a second watchdog process. ", "Its "
         "loop can run 45 minutes / 50 turns — long enough that something watching *while* "
         "it runs, not just between turns, seemed worth the extra layer. MONITOR_EXECUTION "
         "is that layer: a second model, polling the trace, with authority to kill it."),
    ], bullet_size=14.5, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 6
content_slide(prs, 6, TOTAL, LABEL, "format:\"json\"", "A grammar, not a suggestion",
    bullets=[
        ("Grammar-constrained decoding. ", "Every token the model may sample next is "
         "filtered against a JSON grammar before it's chosen — the model is structurally "
         "incapable of emitting anything not on a path to valid JSON."),
        "Genuinely different from how Claude produces structured tool calls — trained to "
        "emit them reliably, not forced into them by masking the token distribution.",
        ("The rule this produces: ", "whenever a model's tool-calling syntax and a "
         "content-format grammar share the same channel, one of them loses — and which one "
         "loses is entirely a property of that model's own template."),
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 7 stat — 0/139 callback
stat_slide(prs, 7, TOTAL, LABEL, "The Sharpest Instance", "0 / 139",
    "tool calls — one model, under a JSON grammar, across its entire recorded history",
    "Its template requires a tool call as literal <tool_call>...</tool_call> text in the "
    "same channel the grammar constrains — and \"<\" is not a legal way to start a JSON "
    "value. Not usually blocked. Structurally, always blocked. (Full story in Part 2.)",
    color=STOP)

# 8
content_slide(prs, 8, TOTAL, LABEL, "Thinking Models", "A stream the grammar can't see into",
    bullets=[
        "A reasoning model produces its chain-of-thought as a distinct pass before "
        "committing to an answer. Measured directly: think:true + format:\"json\" produced "
        "a real thinking block — and message.content came back as exactly {}. Empty.",
        "The grammar mask applies from the first generated token. <think> isn't valid JSON, "
        "so a model that wants to think first has nowhere legal to put it.",
        "The original fleet only worked by accident: gemma4:31b isn't a reasoning model at "
        "all, and qwen3:32b's older template defers the grammar mask until after its own "
        "</think> tag closes. Every newer fast reasoning model broke this coexistence.",
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 9
content_slide(prs, 9, TOTAL, LABEL, "The Fix: Reasoning Inside the Schema", "Almost elegant",
    bullets=[
        "Define an explicit JSON Schema whose first required property is reasoning — a "
        "plain string, before any real structured field. Ollama follows a schema's "
        "declared property order when generating.",
        "The model's chain-of-thought becomes a grammar-legal string value it fills in "
        "before it has to commit to the decision that follows. It still gets to think — the "
        "thinking just has to happen inside the JSON.",
        ("Measured on bead-decomposition, on a model that had been failing outright: ", "2/2 "
         "attempts producing beads: [] under bare \"json\" (63KB, ~17 min each) became one "
         "attempt, ~2 minutes, 9 correctly-structured beads, and a genuine 1,239-character "
         "reasoning field — on the first live run."),
    ], bullet_size=15.5, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 10
content_slide(prs, 10, TOTAL, LABEL, "A Schema Can Cap a String's Length", "At the grammar level",
    bullets=[
        "A schema constrains a string field's type, not its length — a reasoning model that "
        "didn't know when to stop filled its reasoning field to 76KB before an escape "
        "character broke its own JSON.",
        ("The natural assumption: catch and truncate this in application code. ", "Tested "
         "directly instead: Ollama's schema-to-grammar compiler actually enforces JSON "
         "Schema's maxLength at the token level."),
        "A schema capping reasoning at exactly 400 characters, against a prompt demanding "
        "exhaustive analysis: the response came back at exactly 400 characters, valid JSON, "
        "a real verdict attached, no thrashing. The grammar forces the string closed at the "
        "cap and lets the model finish the object around it.",
        "Production cap: 16,000 characters — genuine deep reasoning on the hardest verbs "
        "ran 13–14K unprompted.",
    ], bullet_size=14, top_bullets=Inches(2.0), bullets_height=Inches(4.8))

# 11
content_slide(prs, 11, TOTAL, LABEL, "Where the Trick Stops Working", "A subtle failure mode",
    bullets=[
        "Applied to the verb that writes tests — whose real output is a sequence of "
        "write_function calls, not a structured decision — the schema gave the model a "
        "grammar-legal way to describe a plan and claim the work was done, in prose, "
        "without ever calling the tool.",
        "The completeness gate caught the gap downstream, but the run had already "
        "escalated on a bead that should have been trivial.",
        ("The principle: ", "a reasoning-first schema is for a verb whose output genuinely "
         "IS a structured decision. A verb whose real output is tool calls needs the model "
         "actually reaching for those tools, not narrating an intention to."),
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 12 table — 3 configurations
table_slide(prs, 12, TOTAL, LABEL, "Three Configurations Today", "Confirmed directly against current source",
    headers=["Verb class", "Configuration", "Example verbs"],
    rows=[
        ["Structured-decision", "Reasoning-first schema, thinking explicitly disabled", "SURVEY, CERTIFY, DECOMPOSE, AUDIT, RECONCILE, ANALYZE, REVISE_PENDING"],
        ["Tool-loop", "No format constraint at all — defensive parse-and-repair only", "WRITE, CRITIQUE, JUDGE"],
        ["Native tool-calling", "Plain, unconstrained default format:\"json\"", "ADJUDICATE"],
    ], col_widths=[2.2, 4.3, 5.4], font_size=13, top=Inches(2.3))

# 13 quote — the tension
quote_slide(prs, 13, TOTAL, LABEL, "An Unresolved Tension",
    "The correct configuration for any given verb isn't a property of the verb at all — "
    "it's a property of whichever model happens to be assigned to it this week.",
    "Nothing today re-derives the right configuration automatically when a model gets "
    "swapped in a bakeoff. It has already quietly broken twice — the identical JSON-grammar "
    "conflict, hitting two different newly-tried models, on two separate later occasions.")

# 14
content_slide(prs, 14, TOTAL, LABEL, "The Cache You Don't Have to Think About", "Until the hardware you do",
    bullets=[
        "Ollama reuses the KV cache — the attention layer's memory of everything generated "
        "so far — across turns in one loop, as long as each turn's prompt is simply the "
        "previous one with new tokens appended.",
        "Measured directly: prompt_eval time stays flat, a second or two, even six turns "
        "into a CRITIQUE loop with a steadily growing transcript. The cost of a long loop is "
        "almost entirely generation, not re-processing a growing prompt from scratch.",
        "Part of why an alternative local-inference backend was never worth migrating to — "
        "the efficiency win it would have offered was mostly already there.",
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 15 — claude contrast
two_col_slide(prs, 15, TOTAL, LABEL, "Two Ways to Not Re-Pay for a Prefix", "Same goal, different mechanism",
    left_head="OLLAMA (THIS FLEET)",
    left_items=[
        "Implicit, automatic within one live session.",
        "Not a knob you reach for — a property of how the server already handles a "
        "growing conversation.",
        "No explicit pricing model for cache hits/misses.",
    ],
    right_head="CLAUDE'S API",
    right_items=[
        "Explicit — opted into with deliberate cache_control breakpoints.",
        "Its own write and read pricing.",
        "A defined TTL.",
    ], title_size=28)

# 16 — num_ctx / hardware
content_slide(prs, 16, TOTAL, LABEL, "num_ctx: the Knob That Costs Real GPU", "119 GiB, shared",
    bullets=[
        ("40,960 tokens, ", "not the largest window any fleet model supports — the "
         "smallest one any of them natively trained on. Avoids the quality cost of "
         "stretching past trained context via rope-scaling."),
        "Several ~24–35 GB models load simultaneously — the execution step and its watchdog "
        "run concurrently, by design — sharing one GPU's unified memory pool.",
        "Under that pressure, the GPU gets time-shared, and a generation that should take "
        "seconds can silently stall for minutes waiting its turn.",
        "Calling Claude's API, this category of problem doesn't exist. Calling a fleet of "
        "local models, it's an ordinary Tuesday.",
    ], bullet_size=15, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 17 — stream idle watchdog
content_slide(prs, 17, TOTAL, LABEL, "A Silence the Model Didn't Cause", "The stream itself can die",
    bullets=[
        "Distinct from judging whether a MODEL is making progress: this watchdog judges "
        "whether the STREAM is still alive.",
        "One real run: a model's response went completely silent mid-generation — no "
        "error, no chunk — for over 38 minutes, twice, while Ollama kept responding fine to "
        "everything else. The model wasn't stuck; the transport was.",
        ("The fix: ", "10 minutes tolerated before the first chunk (covers real prompt "
         "eval); 3 minutes tolerated between any two chunks after that (long enough that a "
         "slow thinking stream keeps resetting the clock, unambiguous once real silence sets in)."),
        ("Classified transient, not a strike. ", "A momentarily-busy GPU gets a free retry; "
         "a genuinely offline model still escalates promptly."),
    ], bullet_size=14, top_bullets=Inches(2.0), bullets_height=Inches(4.8))

# 18 NEW — self-audit hook
stat_slide(prs, 18, TOTAL, LABEL, "A Self-Audit", "Dead code",
    "MONITOR_EXECUTION's model-judgment FIRE path has never fired once — a parsing bug",
    "Its model answers {\"DECISION\": \"NO_FIRE\", ...} under the standard JSON constraint. "
    "The code reading that response looks for a line starting with the literal text "
    "\"DECISION:\" — which a JSON object never produces. Every real verdict has silently "
    "fallen through to the hardcoded default.", color=STOP)

# 19 NEW — self-audit detail
content_slide(prs, 19, TOTAL, LABEL, "Does the Monitor Still Earn Its Keep?", "Not as designed",
    bullets=[
        ("What still works: ", "two much narrower mechanical pattern checks bundled into "
         "the same file — a literal repeated tool call, or a stall specifically on "
         "write_file. Today, MONITOR_EXECUTION already IS a mechanical check. It just still "
         "has a model's name on it."),
        ("Its cost was real enough to cause an incident. ", "Uncapped, the monitor's model "
         "once spent 11.5 minutes generating 4,000 tokens to answer a yes/no question — "
         "competing head-to-head with EXECUTE's real work on the same GPU."),
        "That contention stalled EXECUTE's own token delivery, tripped the stream-idle "
        "watchdog, and produced a flatly wrong diagnosis: logged as \"crashed at startup,\" "
        "when a healthy generation had been stalled by its own supervisor.",
        ("The deeper fix — ", "parse the verdict correctly, poll less often, use a smaller "
         "model — is written up and explicitly deferred, not done."),
    ], bullet_size=13.5, top_bullets=Inches(2.0), bullets_height=Inches(4.8))

# 20 — throughline
quote_slide(prs, 20, TOTAL, LABEL, "The Throughline",
    "None of these are bugs in the ordinary sense — they're the actual shape of what it "
    "costs to run a production pipeline on open, self-hosted models instead of a frontier API.",
    "Every mechanic in this deck is something Claude's own infrastructure has already "
    "solved, generalized, and made invisible. Every one of them was, for this fleet, a "
    "specific incident with a specific model, root-caused and fixed by hand — including, "
    "as the last finding shows, the ones that turned out not to be fully solved yet.")

closing_slide(prs, 21, TOTAL, LABEL,
    "How far can process and discipline carry you",
    "when you don't even get to assume the infrastructure underneath the model is fully "
    "solved, either? Most of the actual answer is in this deck.",
    next_label="Write for Zero Domain Knowledge — the craft of writing a spec precise enough "
               "for a 30B model to build correctly on the first pass.")

out = "/Users/mike/Documents/GitHub/ratchet/writing/decks/03-format-think-turn.pptx"
import os
os.makedirs(os.path.dirname(out), exist_ok=True)
prs.save(out)
print("Saved", out, "slides:", len(prs.slides._sldIdLst))

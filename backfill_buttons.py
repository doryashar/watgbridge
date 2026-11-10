#!/usr/bin/env python3
"""One-shot backfill: attach the default reaction keyboard to every already-bridged message.

Reads msg_id_pairs from gobot.sqlite.db, normalizes @lid chat ids to phone-number
form via whatsmeow_lid_map (wawebstore.db, read-only), rebuilds the exact keyboard
the Go builder produces (defaults + counts + info row), and edits each Telegram
message via the Bot API. Adaptive 429 backoff; benign errors skipped.
"""
import json
import re
import sqlite3
import time
import urllib.error
import urllib.request

GBOT = "/home/ubuntu/apps/watgbridge/gobot.sqlite.db"
WA_STORE = "file:/home/ubuntu/apps/watgbridge/wawebstore.db?mode=ro"
CONFIG = "/home/ubuntu/apps/watgbridge/config.yaml"

DEFAULT_REACTIONS = ["👍", "❤️", "😂", "😮", "🙏"]

token_match = re.search(r'bot_token:\s*"?(\d+:[A-Za-z0-9_-]+)"?', open(CONFIG).read())
if not token_match:
    raise SystemExit("bot_token not found in config.yaml")
token = token_match.group(1)


def lid_to_pn(lid_user: str):
    db = sqlite3.connect(WA_STORE, uri=True)
    row = db.execute("SELECT pn FROM whatsmeow_lid_map WHERE lid=?", (lid_user,)).fetchone()
    db.close()
    return row[0] if row else None


def normalize_chat_id(cid: str) -> str:
    if cid.endswith("@lid"):
        pn = lid_to_pn(cid.split("@")[0])
        if pn:
            return pn + "@s.whatsapp.net"
    return cid


pairs_db = sqlite3.connect(f"file:{GBOT}?mode=ro", uri=True)
pairs = pairs_db.execute(
    "SELECT tg_chat_id, tg_msg_id, wa_chat_id, id FROM msg_id_pairs WHERE tg_msg_id != 0"
    " AND tg_msg_id >= 7150"  # 48h edit ceiling (~50h with margin); older = permanently uneditable
    " ORDER BY tg_msg_id DESC"
).fetchall()

# reaction counts keyed by (normalized chat, msg id)
counts = {}
for chat, msg, emoji in pairs_db.execute(
    "SELECT wa_chat_id, wa_msg_id, emoji FROM message_reactions"
):
    counts.setdefault((normalize_chat_id(chat), msg), {}).setdefault(emoji, 0)
    counts[(normalize_chat_id(chat), msg)][emoji] += 1
pairs_db.close()


def build_keyboard(chat: str, msg: str):
    """Callback data carries ONLY the emoji — the handler resolves the
    chat/message from the callback query's own message (Telegram caps
    callback_data at 64 bytes; id-embedded data overflowed on long ids)."""
    c = counts.get((chat, msg), {})
    rows = []
    if c:
        rows.append([{"text": "🙂 Reactions", "callback_data": "reactinfo"}])
    row, extras = [], []
    for e in DEFAULT_REACTIONS:
        text = f"{e} {c[e]}" if c.get(e) else e
        row.append({"text": text, "callback_data": f"react|{e}"})
    rows.append(row)
    for e, n in sorted(c.items(), key=lambda kv: (-kv[1], kv[0])):
        if e not in DEFAULT_REACTIONS:
            extras.append({"text": f"{e} {n}", "callback_data": f"react|{e}"})
    if extras:
        rows.append(extras)
    return {"inline_keyboard": rows}


BENIGN = (
    "message is not modified",
    "message to edit not found",
    "message can't be edited",
    "MESSAGE_ID_INVALID",
    "message to replay not found",
    "reply markup not changed",
)

done = skipped = failed = 0
t0 = time.time()
api = f"https://api.telegram.org/bot{token}/editMessageReplyMarkup"


def edit_one(tg_chat, tg_msg, wa_chat, wa_msg):
    """Returns 'ok' | 'skip' | 'fail'."""
    chat = normalize_chat_id(wa_chat)
    payload = json.dumps({
        "chat_id": tg_chat,
        "message_id": tg_msg,
        "reply_markup": build_keyboard(chat, wa_msg),
    }).encode()
    req = urllib.request.Request(api, data=payload, headers={"Content-Type": "application/json"})
    while True:
        try:
            with urllib.request.urlopen(req, timeout=20) as r:
                body = json.loads(r.read())
            return "ok" if body.get("ok") else "fail"
        except urllib.error.HTTPError as e:
            body = e.read().decode("utf-8", "replace")
            if e.code == 429:
                m = re.search(r'"retry_after":(\d+)', body)
                retry = float(m.group(1)) if m else 5.0
                time.sleep(retry + 1)
                continue
            if any(b in body for b in BENIGN):
                return "skip"
            print(f"FAIL {tg_chat}/{tg_msg}: {body[:160]}", flush=True)
            return "fail"
        except Exception as e:
            print(f"ERR {tg_chat}/{tg_msg}: {e}", flush=True)
            return "fail"


pending = list(pairs)
for pass_no in (1, 2):
    print(f"--- pass {pass_no}: {len(pending)} targets ---", flush=True)
    unresolved = []
    for i, (tg_chat, tg_msg, wa_chat, wa_msg) in enumerate(pending, 1):
        result = edit_one(tg_chat, tg_msg, wa_chat, wa_msg)
        if result == "ok":
            done += 1
        elif result == "skip":
            skipped += 1
        else:
            failed += 1
            unresolved.append((tg_chat, tg_msg, wa_chat, wa_msg))
        if i % 100 == 0:
            rate = i / max(time.time() - t0, 1)
            print(f"[p{pass_no} {i}/{len(pending)}] ok={done} skip={skipped} fail={failed} ({rate:.1f}/s)", flush=True)
        time.sleep(1.1)  # just above Telegram's per-chat edit rate: avoids 429 penalty windows
    pending = unresolved
    if not pending:
        break

print(f"DONE ok={done} skip={skipped} unresolved={len(pending)} elapsed={time.time()-t0:.0f}s", flush=True)

#!/usr/bin/env python3
"""Generates the four architecture diagrams: {en,ru} x {light,dark}.

    python3 docs/architecture.py

One layout, one string table per language, one palette per theme — so the
variants cannot drift apart by hand. Edit THIS file rather than the SVGs: four
pictures kept in step by hand is four chances to leave a reader looking at a
version of the engine that no longer exists, and readme_test.go fails the build
when they stop matching.

Nothing in the library runs this — it is a development tool, and the engine's
own no-dependency rule is unaffected.
"""
import os

SANS = "-apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif"
MONO = "ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, monospace"

PALETTES = {
    "light": dict(
        ink="#1f2328", muted="#59636e", line="#d1d9e0",
        box_fill="#ffffff", box_stroke="#d1d9e0",
        engine_fill="#eef2ff", engine_stroke="#6366f1",
        engine_ink="#3730a3", badge_fill="#e0e7ff",
        port="#0550ae", arrow="#9198a1", frame="#d1d9e0",
    ),
    "dark": dict(
        ink="#e6edf3", muted="#9198a1", line="#3d444d",
        box_fill="#151b23", box_stroke="#3d444d",
        engine_fill="#1c1f3a", engine_stroke="#818cf8",
        engine_ink="#c7d2fe", badge_fill="#282c4d",
        port="#79c0ff", arrow="#6e7681", frame="#3d444d",
    ),
}

STRINGS = {
    "en": dict(
        app="your application",
        parse="ParseSkill",
        badge="stdlib only",
        e1="order of steps · branches · loops · parallel · delegation",
        e2="variables · tool radius · empty & error policy · versioning",
        ports=[
            ("Runner", ["your LLM client"]),
            ("Caller", ["your tools", "(MCP, HTTP)"]),
            ("Delegate", ["your skill", "catalogue"]),
            ("Assets", ["your files"]),
            ("Memory", ["your store"]),
        ],
        o1="vars — what the steps produced   ·   Outcome — a trace of every step",
        o2="OnStepStart / OnStep — events as they happen, for telemetry and UI",
    ),
    "ru": dict(
        app="твоё приложение",
        parse="ParseSkill",
        badge="только stdlib",
        e1="порядок шагов · ветвления · циклы · parallel · делегирование",
        e2="переменные · радиус инструментов · политики пустого и ошибок",
        ports=[
            ("Runner", ["твой клиент к модели"]),
            ("Caller", ["твои инструменты", "(MCP, HTTP)"]),
            ("Delegate", ["твой каталог", "скиллов"]),
            ("Assets", ["твои файлы"]),
            ("Memory", ["твоё хранилище"]),
        ],
        o1="vars — что произвели шаги   ·   Outcome — трасса каждого шага",
        o2="OnStepStart / OnStep — события по ходу дела, для телеметрии и UI",
    ),
}


def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def build(lang, theme):
    s, p = STRINGS[lang], PALETTES[theme]
    o = []
    add = o.append

    add(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 900 570" '
        f'width="900" height="570" role="img" aria-label="skill-engine architecture">')
    add(f'<defs><marker id="a" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" '
        f'markerHeight="7" orient="auto-start-reverse">'
        f'<path d="M0,1 L9,5 L0,9 z" fill="{p["arrow"]}"/></marker></defs>')

    def rect(x, y, w, h, rx, fill, stroke, dash=None):
        d = f' stroke-dasharray="{dash}"' if dash else ""
        add(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" fill="{fill}" '
            f'stroke="{stroke}" stroke-width="1.5"{d}/>')

    def text(x, y, t, size=13, fill=None, family=SANS, anchor="middle", weight="400", ls=""):
        extra = f' letter-spacing="{ls}"' if ls else ""
        add(f'<text x="{x}" y="{y}" font-family="{family}" font-size="{size}" '
            f'font-weight="{weight}" fill="{fill or p["ink"]}" text-anchor="{anchor}"{extra}>{esc(t)}</text>')

    def arrow(x1, y1, x2, y2):
        add(f'<path d="M{x1},{y1} L{x2},{y2}" fill="none" stroke="{p["arrow"]}" '
            f'stroke-width="1.5" marker-end="url(#a)"/>')

    # the application's boundary
    rect(24, 24, 852, 522, 16, "none", p["frame"], dash="7 6")
    text(46, 51, s["app"], size=12.5, fill=p["muted"], anchor="start", ls="0.4")

    # the way in: a file, a parsed skill, the call
    rect(60, 68, 130, 42, 9, p["box_fill"], p["box_stroke"])
    text(125, 94, "skill.yaml", size=13, family=MONO)
    arrow(196, 89, 274, 89)
    text(235, 82, s["parse"], size=11, fill=p["muted"], family=MONO)
    rect(280, 68, 100, 42, 9, p["box_fill"], p["box_stroke"])
    text(330, 94, "Skill", size=13, family=MONO)
    arrow(386, 89, 424, 89)
    rect(430, 68, 330, 42, 9, p["box_fill"], p["box_stroke"])
    text(595, 94, "ExecuteWith(ctx, flow, Deps, vars)", size=13, family=MONO)
    arrow(595, 116, 595, 146)

    # the engine
    rect(60, 152, 780, 126, 14, p["engine_fill"], p["engine_stroke"])
    text(84, 184, "skill-engine", size=17, fill=p["engine_ink"], anchor="start", weight="600")
    add(f'<rect x="712" y="164" width="104" height="24" rx="12" fill="{p["badge_fill"]}" '
        f'stroke="{p["engine_stroke"]}" stroke-width="1"/>')
    text(764, 180, s["badge"], size=11, fill=p["engine_ink"], family=MONO)
    text(450, 222, s["e1"], size=13.5, fill=p["ink"])
    text(450, 248, s["e2"], size=13.5, fill=p["ink"])

    # what the engine cannot do itself
    for i, (name, lines) in enumerate(s["ports"]):
        x = 60 + i * 157
        arrow(x + 76, 284, x + 76, 312)
        rect(x, 316, 152, 78, 10, p["box_fill"], p["box_stroke"])
        text(x + 76, 344, name, size=13, fill=p["port"], family=MONO)
        for j, ln in enumerate(lines):
            text(x + 76, 366 + j * 17, ln, size=12, fill=p["muted"])

    # what comes back
    rect(60, 436, 780, 76, 12, "none", p["box_stroke"], dash="6 5")
    text(450, 467, s["o1"], size=12.5, fill=p["ink"])
    text(450, 491, s["o2"], size=12.5, fill=p["muted"])
    add(f'<path d="M60,215 L42,215 L42,474 L54,474" fill="none" stroke="{p["arrow"]}" '
        f'stroke-width="1.5" marker-end="url(#a)"/>')

    add("</svg>")
    return "\n".join(o) + "\n"


root = os.path.dirname(os.path.abspath(__file__))
for lang in ("en", "ru"):
    for theme in ("light", "dark"):
        suffix = "" if lang == "en" else ".ru"
        path = f"{root}/architecture{suffix}.{theme}.svg"
        open(path, "w").write(build(lang, theme))
        print(path)

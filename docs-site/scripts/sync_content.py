"""Copy the explicit public reference allowlist; never crawl internal notes."""
import json
import re
import sys
from pathlib import Path
site = Path(__file__).resolve().parents[1]
root = site.parent
pages = json.loads((site / "content-manifest.json").read_text())
links = {Path(p["source"]).name: p.get("route", "/docs/" + p["slug"]) for p in pages if Path(p["source"]).name != "README.md"}
links.update({"sysonescript-semantic-plan.md": "/docs/limits", "sysonescript-cli-roadmap.md": "/docs/limits"})
changed = []
for page in pages:
    body = (root / page["source"]).read_text()
    def rewrite(match):
        target = match.group(1)
        if target == "../examples/sos/tickets.sos":
            return "](/docs/tickets-source)"
        if target == "../examples/sos/repo-assistant/README.md":
            return "](/docs/repository-cli)"
        return "](" + links.get(target, target) + ")"
    body = re.sub(r"\]\(([^)]+)\)", rewrite, body)
    destination = site / "content" / (page["slug"] + ".md")
    if not destination.exists() or destination.read_text() != body:
        changed.append(page["slug"])
        if "--check" not in sys.argv:
            destination.write_text(body)
if changed and "--check" in sys.argv:
    raise SystemExit("Stale public content: " + ", ".join(changed))
print("Public references synchronized: " + str(len(pages)))

source_page = "# Tickets CLI source\n\n```text\n" + (root / "examples/sos/tickets.sos").read_text() + "\n```\n"
source_path = site / "content/tickets-source.md"
if "--check" in sys.argv:
    if not source_path.exists() or source_path.read_text() != source_page:
        raise SystemExit("Stale tickets source page")
else:
    source_path.write_text(source_page)

semlint_source = "# SOS semlint source\n\nThe executable example is `examples/sos/semlint/scan.sos`. Read the [guide](/docs/semlint-sos) for usage and parity notes.\n\n```text\n" + (root / "examples/sos/semlint/scan.sos").read_text() + "\n```\n"
semlint_path = site / "content/semlint-source.md"
if "--check" in sys.argv:
    if not semlint_path.exists() or semlint_path.read_text() != semlint_source:
        raise SystemExit("Stale SOS semlint source page")
else:
    semlint_path.write_text(semlint_source)

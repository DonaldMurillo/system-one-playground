"""Verify the public export's pages, local links, assets and content boundaries."""
import json
from html.parser import HTMLParser
from pathlib import Path
import sys
from urllib.parse import unquote, urlsplit
root = Path(sys.argv[1] if len(sys.argv) > 1 else 'dist').resolve()
base = sys.argv[2].rstrip('/') if len(sys.argv) > 2 else ''
errors = []
class Links(HTMLParser):
    def __init__(self):
        super().__init__(); self.links = []
    def handle_starttag(self, tag, attrs):
        for key, value in attrs:
            if key in ('href', 'src') and value:
                self.links.append(value)
def destination(path):
    candidate = root / path.lstrip('/')
    if candidate.is_dir():
        candidate /= 'index.html'
    if not candidate.exists() and not candidate.suffix:
        candidate = candidate.with_suffix('.html')
    return candidate
pages = list(root.rglob('*.html'))
assert len(pages) >= 23, 'Missing exported documentation pages'
for page in pages:
    text = page.read_text()
    if '/Users/dom/' in text or 'barcode-toolkit' in text or 'v0.0.0-dev' in text:
        errors.append(f'{page.relative_to(root)} contains local/internal material')
    parser = Links(); parser.feed(text)
    for link in parser.links:
        url = urlsplit(link)
        if url.scheme or url.netloc or not url.path or url.path.startswith('/mcp'):
            continue
        path = unquote(url.path)
        if path.startswith('/'):
            if base:
                if not path.startswith(base + '/') and path != base:
                    errors.append(f'{page.relative_to(root)}: missing base in {link}'); continue
                path = path[len(base):]
            target = destination(path)
        else:
            target = page.parent / path
        if not target.exists():
            errors.append(f'{page.relative_to(root)}: missing {link}')
for name in ['sitemap.xml', 'robots.txt', 'manifest.webmanifest', 'service-worker.js', '404.html', 'llms.txt', '.well-known/agent-card.json', '__fastr-docs/search.json']:
    if not (root / name).is_file(): errors.append('Missing ' + name)
search = root / '__fastr-docs/search.json'
if search.exists():
    data = search.read_text(); json.loads(data)
    for term in ['repository-cli', 'configuration', 'agent-interface', '/client', '/semlint', '/studio', '/tools']:
        if term not in data: errors.append('Search lacks ' + term)
    for term in ['agent-notes', 'build-openapi', 'concepts-router']:
        if term in data: errors.append('Unexpected search content: ' + term)
if errors:
    raise SystemExit('\n'.join(sorted(set(errors))))
print(f'Export verified: {len(pages)} HTML pages, local links/assets, search and agent surfaces')

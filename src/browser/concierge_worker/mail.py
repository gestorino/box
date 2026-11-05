"""The email a site sent to the owner's address, as the model reads it.

A confirmation link or a one-time code in an email is as good as a password while it is fresh, so
neither reaches the prompt: each link is shown as `[link N: <its words>]` and each 4–8 digit code
as `[code N]`, and the agent's actions open or type them by number from the lists kept here.
"""

from __future__ import annotations

import json
import re
from html.parser import HTMLParser

_CODE = re.compile(r'(?<!\d)\d{4,8}(?!\d)')
_URL = re.compile(r'https?://[^\s<>"\')]+', re.I)
_SKIP = {'script', 'style', 'head', 'title'}
_BLOCK = {'p', 'div', 'br', 'tr', 'li', 'h1', 'h2', 'h3', 'h4', 'table'}


class _Text(HTMLParser):
    """The html's text with every http(s) link as its placeholder, numbered into `links`."""

    def __init__(self, links: list[str]) -> None:
        super().__init__(convert_charrefs=True)
        self.links = links
        self.out: list[str] = []
        self._skip = 0
        self._href: str | None = None
        self._words: list[str] = []

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        if tag in _SKIP:
            self._skip += 1
        elif tag in _BLOCK:
            self.out.append('\n')
        if tag == 'a':
            href = (dict(attrs).get('href') or '').strip()
            self._href = href if re.match(r'https?://', href, re.I) else None
            self._words = []

    def handle_endtag(self, tag: str) -> None:
        if tag in _SKIP and self._skip:
            self._skip -= 1
        if tag == 'a' and self._href is not None:
            self.links.append(self._href)
            # Words that are themselves a URL ("copy this link") would show the link: never shown.
            words = ' '.join(_URL.sub(' ', ' '.join(self._words)).split())
            self.out.append(f'[link {len(self.links)}: {words}]' if words else f'[link {len(self.links)}]')
            self._href = None

    def handle_data(self, data: str) -> None:
        if self._skip:
            return
        if self._href is not None:
            self._words.append(data)
        else:
            self.out.append(data)


def _tidy(text: str) -> str:
    lines = (' '.join(line.split()) for line in text.splitlines())
    return '\n'.join(line for line in lines if line)


def view(raw: str) -> tuple[str, list[str], list[str]]:
    """What the model is shown of the engine's email (JSON `{"subject","text","html"}`), and the
    links and codes its placeholders stand for, numbered from 1. An unreadable value is an empty
    email: the model is told nothing came worth reading, never an error quoting it."""
    try:
        mail = json.loads(raw)
        subject, text, html = (str(mail.get(k) or '') for k in ('subject', 'text', 'html'))
    except (ValueError, AttributeError):
        subject = text = html = ''
    links: list[str] = []
    parser = _Text(links)
    parser.feed(html)
    parser.close()
    if text.strip():
        body = text
        if links:
            body += '\n\nLinks: ' + ' '.join(o for o in parser.out if o.startswith('[link '))
    else:
        body = ''.join(parser.out)

    def link(match: re.Match[str]) -> str:
        url = match.group(0)
        if url not in links:
            links.append(url)
        return f'[link {links.index(url) + 1}]'

    # Every URL left anywhere — the subject, the text, a bare one in the html — is a link too, by
    # number: none reaches the model.
    shown = _URL.sub(link, f'Subject: {" ".join(subject.split())}\n\n{_tidy(body)}')
    codes: list[str] = []

    def code(match: re.Match[str]) -> str:
        codes.append(match.group(0))
        return f'[code {len(codes)}]'

    # The subject too: "Your code is 551204" is a common one.
    return _CODE.sub(code, shown), links, codes

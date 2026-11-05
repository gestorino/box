"""No code of the worker names a site (CLAUDE.md, Hard conventions).

The model reaches a goal the same way on every site, so nothing it runs may name one. The names are
those of the sites the box has had cards for, `testdata/site-names.txt`: each name and its host. What
is read is the code: every identifier and string literal of the worker's source. Comments and
docstrings are the record of what each piece was learned on, and the model never reads them. The engine's half is `internal/signins/nosite_test.go`.
"""

from __future__ import annotations

import ast
from pathlib import Path

_REPO = Path(__file__).resolve().parents[3]
_WORKER = _REPO / 'src' / 'browser' / 'concierge_worker'


def _site_names() -> set[str]:
    names: set[str] = set()
    for line in (_REPO / 'testdata' / 'site-names.txt').read_text(encoding='utf-8').splitlines():
        *name, host = line.split() or ['']
        if line.startswith('#') or not name:
            continue
        names |= {' '.join(name), host, host.removeprefix('www.')}
    return {name.casefold() for name in names}


def _docstrings(tree: ast.AST) -> set[int]:
    held = (ast.Module, ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef)
    return {id(node.body[0].value) for node in ast.walk(tree)
            if isinstance(node, held) and node.body and isinstance(node.body[0], ast.Expr)
            and isinstance(node.body[0].value, ast.Constant) and isinstance(node.body[0].value.value, str)}


def _code_words(tree: ast.AST) -> list[tuple[int, str]]:
    """Every identifier and string literal, with its line: what the worker runs and says."""
    skipped = _docstrings(tree)
    words: list[tuple[int, str]] = []
    for node in ast.walk(tree):
        line = getattr(node, 'lineno', 0)
        if isinstance(node, ast.Constant) and isinstance(node.value, str) and id(node) not in skipped:
            words.append((line, node.value))
        elif isinstance(node, ast.Name):
            words.append((line, node.id))
        elif isinstance(node, ast.Attribute):
            words.append((line, node.attr))
        elif isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            words.append((line, node.name))
        elif isinstance(node, ast.arg):
            words.append((line, node.arg))
        elif isinstance(node, ast.keyword) and node.arg:
            words.append((line, node.arg))
        elif isinstance(node, ast.alias):
            words.append((line, node.asname or node.name))
    return words


def test_the_names_come_from_the_site_names_list():
    names = _site_names()
    assert len(names) >= 3
    assert all(names)


def test_no_identifier_or_string_of_the_worker_names_a_site():
    names = _site_names()
    found = [f'{path.relative_to(_REPO)}:{line}: {word!r}'
             for path in sorted(_WORKER.rglob('*.py'))
             for line, word in _code_words(ast.parse(path.read_text(encoding='utf-8')))
             for name in names if name in word.casefold()]
    assert found == []

"""The browser worker process.

A service of its own: binds a Unix socket the engine connects to, and answers on the contract in
`src/engine/internal/browser/worker.go` and `ipc.py`. Nothing here is imported by the Go side:
the socket is the whole interface between the two.
"""

import os

# Before any `browser_use` import anywhere in the worker: their config reads these lazily, per access.
# Assigned, not `setdefault`ed: a local-first appliance never ships a screenshot or a task string to
# anybody's cloud, and that is not an operator preference an inherited environment gets to flip.
os.environ['ANONYMIZED_TELEMETRY'] = 'false'
os.environ['BROWSER_USE_CLOUD_SYNC'] = 'false'

"""The names a run asks the engine under, mid-run (`value_request`), and what answers them.

`human_check` carries no value: a person has to act in the window — solve a captcha the gate will
not let the model touch — and the answer only means "done, look again". `question` is an agent run's
question to the owner, with the choices the page offers. `sms_code` and `email` are what the box
receives for the owner's account: the code an SMS to its SIM brought, and the site's email.
"""

from __future__ import annotations

import re
from typing import Awaitable, Protocol

#: The name that asks for a person rather than a value.
HUMAN_CHECK = 'human_check'

# Characters that render as nothing and split a word for a matcher: soft hyphen, the zero-width
# space/non-joiner/joiner, the word joiner, and a stray BOM. Stripped wherever page text, which the
# page's author chooses, meets a rule.
_INVISIBLE = re.compile('[\u00ad\u200b-\u200d\u2060\ufeff]')

#: The name an agent run asks the owner a choice under: `why` is the question, `options` its choices.
QUESTION = 'question'

#: The names a run on the owner's account asks the engine under: the code an SMS to the box's SIM
#: brought, and the email the site sent to the account's address (JSON subject, text and html).
SMS_CODE = 'sms_code'
EMAIL = 'email'


class ValueSource(Protocol):
    """`(name, why, options) -> value`, or None when nobody could supply it. `options` is what the
    owner may pick from (a run's question); nothing else sends any."""

    def __call__(self, name: str, why: str, options: list[str] | None = None) -> Awaitable[str | None]: ...

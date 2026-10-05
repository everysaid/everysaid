"""Failures the user is told about.

`UserError(code, status, **params)`: the interface says it in the user's language from the code
(`web/src/lib/i18n.ts`, `errors.<code>`; `tests/test_i18n.py` checks that each code is there). A
plugin, whose words the interface does not know, gives an English `text` instead, which the server
says in the user's language through `plugins/i18n.py` (code `plugin`).
"""


class UserError(Exception):
    def __init__(self, code, status=400, text=None, **params):
        super().__init__(text or code)
        self.code, self.status, self.text, self.params = code, status, text, params


def plugin_error(text, status=409):
    """A plugin's failure, in English (translated on the way out)."""
    return UserError("plugin", status, text=text)

#!/bin/sh
# Notify the operator that a Claude Code session has gone idle.
#
# Routed through herdr, which works on macOS and Linux alike. Outside herdr
# there is nothing to notify through, so this exits quietly rather than
# failing a hook.
[ "${HERDR_ENV:-}" = "1" ] || exit 0
command -v herdr >/dev/null 2>&1 || exit 0

herdr notification show "Claude is waiting" \
  --body "${PWD##*/} — the session needs you" \
  --sound request >/dev/null 2>&1 || true
exit 0

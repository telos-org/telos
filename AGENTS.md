# Repository Instructions

## Do not edit the CLI guide

`skills/telos-cli/` is the Telos CLI guide, rendered at `usetelos.ai/docs`. It
is written by hand. **Agents do not add, edit, move, or delete anything in it,
or draft replacement prose for it**, including as part of a CLI change.

The only exception is an explicit request, from the person you are working
with, for a specific change there.

## Flag documentation impact

When a pull request changes anything a CLI user can see, including validation
and output, add a **Documentation impact** section to its description:

- each page and section the change makes inaccurate, by path and heading;
- the sentence or example that is now wrong, quoted;
- what is now true, as plain facts.

The change is not ready to release until the guide matches it.

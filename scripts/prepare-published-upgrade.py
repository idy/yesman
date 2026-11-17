#!/usr/bin/env python3
"""Run the exact inline, checkout-free published naming-pair downloader locally.

The privileged Actions job is self-contained and never executes a repository
script. This convenience entry and its offline contract tests extract that same
Python block, so there is only one implementation to review and maintain.
"""
import pathlib
import sys
import textwrap


def downloader_source():
    workflow = pathlib.Path(__file__).resolve().parents[1] / '.github/workflows/application-upgrade-draft.yml'
    text = workflow.read_text(encoding='utf-8')
    start = text.index("          # BEGIN PUBLISHED NAMING DOWNLOADER\n")
    end = text.index("          # END PUBLISHED NAMING DOWNLOADER\n", start)
    block = text[start:end]
    source = block.split("<<'PY'\n", 1)[1].rsplit('\n          PY', 1)[0]
    return textwrap.dedent(source)


if __name__ == '__main__':
    exec(compile(downloader_source(), '<inline published naming downloader>', 'exec'))

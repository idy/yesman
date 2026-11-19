#!/usr/bin/env python3
"""Generate only the QA branch's version of the already-registered smoke workflow."""
from pathlib import Path
KIT=Path(__file__).resolve().parent
ROOT=KIT.parents[1]
header='''name: Score CSV final package pair (QA only)
on:
  workflow_dispatch:
    inputs:
      expected_binding:
        description: Exact publisher-approved QA commit and final draft identities as JSON
        required: true
        type: string
permissions:
  contents: read
concurrency:
  group: score-csv-release-051
  cancel-in-progress: false
jobs:
  download:
    if: github.ref == 'refs/heads/qa/model-score-release-051' && github.repository == 'idy/yesman' && github.run_attempt == 1
    runs-on: ubuntu-latest
    timeout-minutes: 8
    permissions:
      contents: write
    steps:
      - name: GET-only exact public and draft acquisition (no checkout)
        env:
          GH_TOKEN: ${{ github.token }}
          EXPECTED_BINDING: ${{ inputs.expected_binding }}
        run: |
          python3 -I - <<'PY'
'''
body=(KIT/'validation.py').read_text()+'\n'+(KIT/'acquire.py').read_text()
footer='''          PY
      - uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02
        with:
          name: score-pair-inputs
          path: ${{ runner.temp }}/score-pair-inputs
          if-no-files-found: error
          retention-days: 7
  native:
    needs: download
    runs-on: macos-latest
    timeout-minutes: 12
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with:
          ref: ${{ github.sha }}
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e
        with:
          go-version-file: go.mod
      - uses: actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093
        with:
          name: score-pair-inputs
          path: ${{ runner.temp }}/score-pair-inputs
      - name: Verify official packages, replace private installation, and cold reopen
        env:
          RUNNER_ENVIRONMENT: ${{ runner.environment }}
          EXPECTED_BINDING: ${{ inputs.expected_binding }}
        run: python3 -B qa/score-release051/run-native.py
      - name: Save bounded native proof
        if: always()
        uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02
        with:
          name: score-pair-proof
          path: ${{ runner.temp }}/score-pair-run/evidence
          if-no-files-found: error
          retention-days: 14
'''
(ROOT/'.github/workflows/macos-draft-smoke.yml').write_text(header+''.join(('          '+l if l else '')+'\n' for l in body.splitlines())+footer)

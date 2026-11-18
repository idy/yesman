"""Fresh synthetic Crew overlay and bounded readonly bridge admission.
Historical adapter packets stay byte-for-byte sealed. No auth/config dumps.
"""
import copy
import hashlib
import json
from pathlib import Path
import re

KEY = 't199-fixture/fixture:off'


def need(value, why):
    if not value:
        raise ValueError(why)


def fresh_seed(original):
    """Change exactly two App Crew fields, leaving pi connection bytes intact."""
    plan = copy.deepcopy(original)
    settings_path = str(Path(plan['data']) / 'settings.json')
    raw = plan['files'][settings_path]
    settings = json.loads(raw)
    expected = {'autoFetch': 0, 'appearance': 'light', 'crew': {'options': [
        {'provider': 't199-fixture', 'model': 'fixture', 'effort': '',
         'score': {'intelligence': 1}}], 'yesman': 't199-fixture/fixture'}}
    need(settings == expected, 'original fresh fixture settings differ')
    settings['crew']['options'][0]['effort'] = 'off'
    settings['crew']['yesman'] = KEY
    corrected = (json.dumps(settings, indent=2) + '\n').encode()
    plan['files'][settings_path] = corrected
    digest = lambda value: hashlib.sha256(value).hexdigest()
    receipt = {'schema': 1, 'scope': 'fresh-synthetic-App-Crew-only',
        'originalSettingsSHA256': digest(raw), 'settingsSHA256': digest(corrected),
        'changes': ['crew.options[0].effort: empty -> off',
                    'crew.yesman: t199-fixture/fixture -> ' + KEY],
        'otherSeedFiles': [{'role': Path(name).name, 'bytes': len(body), 'sha256': digest(body)}
                           for name, body in sorted(plan['files'].items()) if name != settings_path]}
    return plan, receipt


# Only the page sees the real Crew DTO. The report boundary gets this fixed
# synthetic projection, never raw catalog/settings/connection/error values.
BRIDGE = r'''
  let fixtureCrewAdmission;
  async function fixtureAdmission(about) {
    if (about.piState !== "ready") return null;
    if (fixtureCrewAdmission !== undefined) return fixtureCrewAdmission;
    try {
      const v = await call("Crew");
      const models = Array.isArray(v?.models) ? v.models : [];
      const members = Array.isArray(v?.members) ? v.members : [];
      const modelsFound = models.filter(m => m.provider === "t199-fixture" && m.id === "fixture");
      const membersFound = members.filter(m => m.provider === "t199-fixture" && m.model === "fixture");
      const model = modelsFound[0], member = membersFound[0];
      const expectedKey = "t199-fixture/fixture:off";
      const knownLevels = ["off", "minimal", "low", "medium", "high", "xhigh", "max"];
      const levels = Array.isArray(model?.levels) ? model.levels : [];
      const supportedLevels = knownLevels.filter(l => levels.includes(l));
      const readAt = typeof v?.readAt === "string" &&
        /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?(?:Z|[+-]\d\d:\d\d)$/.test(v.readAt) &&
        !v.readAt.startsWith("0001-") ? v.readAt : null;
      fixtureCrewAdmission = {
        schema: 1, status: v?.error ? "catalog-error" : "observed", readAt,
        expectedKey, selected: v?.yesman === expectedKey ? "expected" : "mismatched-or-missing",
        modelCount: Math.min(modelsFound.length, 2), supportedLevels,
        memberCount: Math.min(membersFound.length, 2),
        memberKeyMatches: member?.key === expectedKey, effortMatches: member?.effort === "off",
        memberMissing: member?.missing === true,
        usable: membersFound.length === 1 && member?.key === expectedKey && member?.missing !== true
      };
    } catch (_) {
      fixtureCrewAdmission = {schema: 1, status: "bridge-error"};
    }
    return fixtureCrewAdmission;
  }
'''


def driver(base):
    old = '''  async function observe() {
    // Do not use Messages to inspect paused agents: it starts pi.
    return { about: await call("About"), state: await call("State"),
      update: await call("Update"), restart: await call("RestartStatus") };
  }'''
    new = '''  async function observe() {
    // Do not use Messages to inspect paused agents: it starts pi.
    const about = await call("About");
    return { visibility: document.visibilityState,
      pageReady: !!document.querySelector('#settings-button'), about,
      state: await call("State"), update: await call("Update"),
      restart: await call("RestartStatus"), modelAdmission: await fixtureAdmission(about) };
  }'''
    need(base.count(old) == 1, 'sealed driver observation seam changed')
    return base.replace(old, BRIDGE + new, 1)


def retain_admission(value):
    """Re-project before persistence, then fail closed before the first Hire."""
    row = {'schema': 1, 'status': 'unavailable'}
    if isinstance(value, dict):
        row['status'] = value.get('status') if value.get('status') in (
            'observed', 'catalog-error', 'bridge-error') else 'invalid'
        row['expectedKey'] = KEY
        row['selected'] = 'expected' if value.get('selected') == 'expected' else 'mismatched-or-missing'
        read_at = value.get('readAt')
        row['readAt'] = read_at if isinstance(read_at, str) and re.fullmatch(
            r'\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?(?:Z|[+-]\d\d:\d\d)', read_at) and not read_at.startswith('0001-') else None
        for key in ('modelCount', 'memberCount'):
            row[key] = value.get(key) if type(value.get(key)) is int and 0 <= value[key] <= 2 else 0
        row['supportedLevels'] = [level for level in ('off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max')
                                  if isinstance(value.get('supportedLevels'), list) and level in value['supportedLevels']]
        for key in ('memberKeyMatches', 'effortMatches', 'usable'):
            row[key] = value.get(key) is True
        row['memberMissing'] = value.get('memberMissing') is not False
    row['admitted'] = (row['status'] == 'observed' and row.get('readAt') is not None and
        row['selected'] == 'expected' and row['modelCount'] == row['memberCount'] == 1 and
        row['supportedLevels'] == ['off'] and row['memberKeyMatches'] and row['effortMatches'] and
        not row['memberMissing'] and row['usable'])
    return row

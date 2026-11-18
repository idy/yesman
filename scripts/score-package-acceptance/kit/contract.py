"""Fixed, structured acceptance inputs and report assertions; no application launch."""
import hashlib
import json
from pathlib import Path

SOURCE = 'dfc9ad753ab97fae0b78104bd72d0392282a4ab5'
TREE = '4f8b8f55684e44a997294481768b06b2b64a379f'
HEADER = 'provider,model_id,effort,intelligence_index,tok_per_s,coding_mean_pct,cost_per_task_usd,time_min_per_task\n'
CSV = (HEADER + 'fixture,review-route/qa-csv-fixture,off,0,,,0,\n'
       + 'fixture,review-route/qa-patch-only,off,,37.125,,,0\n').encode()
KEYS = ['fixture/review-route/qa-csv-fixture:off', 'fixture/review-route/qa-patch-only:off']
SETTINGS = (json.dumps({'appearance': 'dark', 'autoFetch': 0, 'crew': {
    'options': [{'provider': 'fixture', 'model': 'review-route/qa-csv-fixture', 'effort': 'off'},
                {'provider': 'fixture', 'model': 'review-route/qa-patch-only', 'effort': 'off'}],
    'yesman': KEYS[0]}}, indent=2) + '\n').encode()
STAGES = {'A': {'date': '2099-01-01', 'baseIntelligence': 45, 'speed': 120, 'coding': 50, 'minutes': 9},
          'B': {'date': '2099-01-02', 'baseIntelligence': 85, 'speed': 210, 'coding': 60, 'minutes': 7}}

def sha(data):
    return hashlib.sha256(data).hexdigest()

def seed(home):
    home = Path(home)
    home.mkdir(mode=0o700)
    for name, data in [('settings.json', SETTINGS), ('model-score-patch.csv', CSV)]:
        target = home / name
        target.write_bytes(data)
        target.chmod(0o600)

def check_files(home):
    home = Path(home)
    assert (home/'model-score-patch.csv').read_bytes() == CSV, 'patch bytes changed'
    assert (home/'settings.json').read_bytes() == SETTINGS, 'settings bytes changed'
    return {'patchSHA256': sha(CSV), 'settingsSHA256': sha(SETTINGS)}

def check_service(report, stage, home):
    home = str(Path(home).resolve())
    expected = STAGES[stage]
    state, settings, crew = (report[k] for k in ('state', 'settings', 'crew'))
    assert state['home'] == home and not state['yesmen'] and not state['agents']
    assert settings['home'] == home and settings['settingsFile'] == str(Path(home)/'settings.json')
    assert settings['appearance'] == 'dark' and settings['autoFetch'] == 0
    assert crew['date'] == expected['date'] and not crew.get('error')
    assert crew['patch'] == {'path': str(Path(home)/'model-score-patch.csv'), 'rows': 2, 'enabled': True}
    assert crew['yesman'] == KEYS[0] and len(crew['models']) == 2
    assert [m['key'] for m in crew['members']] == KEYS
    mixed, partial = crew['members']
    assert not mixed.get('missing') and not partial.get('missing')
    assert mixed['source'] == 'patch' and mixed['fallbackSource'] == 'aa'
    assert mixed['score']['intelligence'] == 0 and mixed['score']['costPerTask'] == 0
    assert not mixed['score'].get('intelligenceMissing')
    assert mixed['score']['speed'] == expected['speed'], 'current package default was not inherited'
    assert mixed['score']['coding'] == expected['coding'] and mixed['score']['minutesPerTask'] == expected['minutes']
    assert mixed['fieldSources'] == {'intelligence': 'patch', 'speed': 'aa', 'coding': 'aa', 'costPerTask': 'patch', 'minutesPerTask': 'aa'}
    assert partial['source'] == 'patch' and partial['score']['intelligenceMissing']
    assert partial['score']['speed'] == 37.125 and partial['score']['minutesPerTask'] == 0
    assert partial['score'].get('coding') is None and partial['score'].get('costPerTask') is None
    assert partial['fieldSources'] == {'speed': 'patch', 'minutesPerTask': 'patch'}

def check_native(report, stage, home):
    assert report['ok'] and not report['errors'], report
    assert report['calls'] == ['Yesman.State', 'Yesman.Settings', 'Yesman.About', 'Yesman.Update', 'Yesman.Crew']
    assert report['visibility'] == 'visible' and 'AppleWebKit/' in report['userAgent']
    assert report['about']['version'] == '0.1.45' and not report['update']['enabled']
    check_service(report, stage, home)
    rows = report['rows']
    assert [r['key'] for r in rows] == KEYS and all(len(r['metrics']) == 5 for r in rows)
    csv_labels = ('Your CSV', '你的 CSV')
    aa_labels = ('Inherited from AA', '继承自 AA')
    assert rows[0]['metrics'][0].startswith('0') and any(s in rows[0]['metrics'][0] for s in csv_labels)
    assert str(STAGES[stage]['speed'])+' tok/s' in rows[0]['metrics'][1]
    assert any(s in rows[0]['metrics'][1] for s in aa_labels)
    assert '—' in rows[1]['metrics'][0]
    assert '37.13 tok/s' in rows[1]['metrics'][1] and any(s in rows[1]['metrics'][1] for s in csv_labels)
    assert rows[1]['metrics'][4].startswith('0 min') and any(s in rows[1]['metrics'][4] for s in csv_labels)

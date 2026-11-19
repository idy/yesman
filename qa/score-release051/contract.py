"""Final public .50 -> final draft .51 persistence contract; no launch code."""
import hashlib,json
from pathlib import Path
BASE_SOURCE='87c3bc4faa0a55313cd68be77058fea166beca82'
BASE_RELEASE_ID=450776814
HEADER='provider,model_id,effort,intelligence_index,tok_per_s,coding_mean_pct,cost_per_task_usd,time_min_per_task\n'
ASTRA='codex-remote/codex/gpt-6.2-astra'
SOL='codex-remote/codex/gpt-6.2-sol'
CSV=(HEADER+'fixture,'+ASTRA+',high,0,,,0,\n'+'fixture,'+SOL+',high,,123.5,,,0\n'+'fixture,local/patch-only,off,,37.125,,,0\n').encode()
OPTIONS=[{'provider':'fixture','model':m,'effort':e} for m,e in [(ASTRA,'high'),(ASTRA,'xhigh'),(SOL,'high'),(SOL,'xhigh'),('local/patch-only','off')]]
KEYS=['fixture/'+o['model']+':'+o['effort'] for o in OPTIONS]
SETTINGS=(json.dumps({'appearance':'dark','autoFetch':0,'scorePatchDisabled':False,'crew':{'options':OPTIONS,'yesman':KEYS[0]}},indent=2)+'\n').encode()
FIELDS=['intelligence','speed','coding','costPerTask','minutesPerTask']
BASE_VALUES=[[60.1,60.7,53.6,2.07,4.9],[61.6,59.9,56.5,2.52,6.2],[54.9,79.3,49.4,.56,4.2],[55.7,80.2,49.4,.65,5.1]]
FINAL_VALUES=[[0,60.7,53.6,0,4.9],BASE_VALUES[1],[54.9,123.5,49.4,.56,0],BASE_VALUES[3]]
def sha(b):return hashlib.sha256(b).hexdigest()
def seed(data):
 data.mkdir(mode=0o700)
 for name,body in [('settings.json',SETTINGS),('model-score-patch.csv',CSV)]:
  p=data/name;p.write_bytes(body);p.chmod(0o600)
def files(data):
 assert (data/'settings.json').read_bytes()==SETTINGS,'settings bytes changed'
 assert (data/'model-score-patch.csv').read_bytes()==CSV,'CSV bytes changed'
 return {'settingsSHA256':sha(SETTINGS),'csvSHA256':sha(CSV)}
def verify_page(p,stage,data,source):
 assert stage in ['old','new','cold'] and p['ok'] and not p['errors'],p
 assert p['visibility']=='visible' and 'AppleWebKit/' in p['userAgent']
 assert p['calls']==['Yesman.State','Yesman.Settings','Yesman.About','Yesman.Update','Yesman.Crew']
 assert p['state']['home']==str(data) and not p['state']['agents'] and not p['state']['yesmen']
 assert p['settings']['settingsFile']==str(data/'settings.json') and p['settings']['appearance']=='dark' and p['settings']['autoFetch']==0
 assert p['about']['version']==('0.1.50' if stage=='old' else '0.1.51') and p['about']['revision']==source
 c=p['crew'];assert c['date']=='2026-11-18' and not c.get('error') and c['yesman']==KEYS[0]
 assert [x['key'] for x in c['members']]==KEYS and len(c['models'])==3
 expected=BASE_VALUES if stage=='old' else FINAL_VALUES
 for i,w in enumerate(expected):
  m=c['members'][i];assert not m.get('missing') and [m['score'][k] for k in FIELDS]==w,(i,m,w)
  assert m['score']['effort']==OPTIONS[i]['effort']
 if stage=='old':
  assert 'patch' not in c and c['members'][4].get('score') is None
  assert all(m['source']=='aa' for m in c['members'][:4])
 else:
  assert c['patch']=={'path':str(data/'model-score-patch.csv'),'rows':3,'enabled':True}
  for i in [0,2]:
   m=c['members'][i];assert m['source']=='patch' and m['fallbackSource']=='aa'
   fields={k:('patch' if (i==0 and k in ['intelligence','costPerTask']) or (i==2 and k in ['speed','minutesPerTask']) else 'aa') for k in FIELDS}
   assert m['fieldSources']==fields
  for i in [1,3]:assert c['members'][i]['source']=='aa'
  partial=c['members'][4];assert partial['source']=='patch' and partial['score']['intelligenceMissing']
  assert partial['score']['speed']==37.125 and partial['score']['minutesPerTask']==0
  assert partial['score'].get('coding') is None and partial['score'].get('costPerTask') is None
  assert partial['fieldSources']=={'speed':'patch','minutesPerTask':'patch'}
 rows=p['rows'];assert [r['key'] for r in rows]==KEYS and all(len(r['metrics'])==5 for r in rows)
 if stage!='old':
  assert rows[0]['metrics'][0].startswith('0') and 'Your CSV' in rows[0]['metrics'][0]
  assert '60.7 tok/s' in rows[0]['metrics'][1] and 'Inherited from AA' in rows[0]['metrics'][1]
  assert '123.5 tok/s' in rows[2]['metrics'][1] and 'Your CSV' in rows[2]['metrics'][1]
  assert rows[2]['metrics'][4].startswith('0 min') and 'Your CSV' in rows[2]['metrics'][4]
  assert '—' in rows[4]['metrics'][0] and '37.13 tok/s' in rows[4]['metrics'][1]
 return files(data)

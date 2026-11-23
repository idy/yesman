#!/usr/bin/env python3
"""Validate the numbered receipts after independently binding the artifact to its run."""
import argparse
from hashlib import sha256
import json
from pathlib import Path
import re

from binding import EPISODES, QA_REF
from hosted import APPROVALS, BULK, BULK_OFFER, LABELS, LOSS_WARNINGS, RECOVERY_STORES, SCENARIOS, SCOPE, recovery_path_receipt, recovery_review
GIT_LOSS = 'This deletes local Git history, branches, tags and objects at these paths. Other checkouts that point here may stop working. Outside repositories, including bare repositories, may also stop working if they borrow Git objects from these stores.'


def read(folder, name):
    return json.loads((folder/f'{name}.json').read_bytes())


def validate(folder, contract, run_id, qa_head, episode, contract_sha256):
    assert episode in EPISODES and re.fullmatch(r'[1-9]\d*', run_id)
    assert re.fullmatch(r'[0-9a-f]{40}',qa_head) and is_sha256(contract_sha256)
    expected = ['cleanup'] if episode == 'cleanup-only' else ['ordinary','seven','recovery-live','recovery-cold']
    assert not (folder/'failure.json').exists() and not (folder/'deletion-episode-failure.json').exists()
    package = read(folder,'package')
    assert package['contract']==contract
    actual=package['archiveReceipt']
    provenance = {'runID':run_id,'runAttempt':'1','episode':episode,'source':contract['source'],
                  'releaseID':contract['releaseID'],'contractSHA256':contract_sha256,'qaRef':QA_REF}
    assert all(actual[k]==v for k,v in provenance.items()) and actual['qaHead']==qa_head
    assert (actual['tag'],actual['arch'],actual['draft'])==(contract['tag'],contract['arch'],True)
    assert (actual['asset']['id'],actual['manifestID'],actual['sha256'])==(contract['assetID'],contract['manifestID'],contract['archiveSHA256'])
    assert package['bundle']['binarySHA256']==contract['binarySHA256']
    overall=read(folder,'episode-pass')
    assert overall==dict(provenance,qaHead=qa_head,completedScenarios=expected,overallPackageAcceptance=False,**SCOPE)
    assert {p.name for p in folder.iterdir() if p.is_dir()}==set(expected)
    validated={}
    recovery={}
    pids=set()
    for name in expected:
        root=folder/name
        assert not (root/'failure.json').exists()
        start=read(root,'initial-disk')
        passed=read(root,'pass')
        assert passed==dict(provenance,scenario=name,completedScenarios=[name],overallPackageAcceptance=False,
                            assetID=contract['assetID'],binarySHA256=contract['binarySHA256'],runSHA=qa_head,
                            **SCOPE,stages=list(SCENARIOS[name]))
        window=read(root,'window')
        pid=window['pid']
        assert isinstance(pid,int) and pid>0 and pid not in pids
        pids.add(pid)
        assert window['processStart'] and window['inode']>0 and window['binarySHA256']==contract['binarySHA256']
        assert any(w['onScreen'] and w['bounds']['Width']>100 and w['bounds']['Height']>100 for w in window['windows'])
        assert read(root,'exit')=={'pid':pid,'exitCode':0} and read(root,'postexit-windows')=={'pid':pid,'windows':[]}
        assert read(root,'postexit-bundle')=={'inventoryUnchanged':True,'strictSignatureVerified':True}
        instrument=read(root,'instrumentation')
        assert set(instrument)=={'index.html','app.js'}
        assert all(r['packaged']==contract['webFiles'][p] and r['observer']['sha256']!=r['packaged']['sha256'] for p,r in instrument.items())
        numbered={p.name for p in root.glob('[0-9][0-9]*.json')}
        expected_names={f'{i:02}{suffix}.json' for i in range(1,len(SCENARIOS[name])+1) for suffix in ('','.disk')}
        assert numbered==expected_names
        bound={}
        last=-1
        for i,step in enumerate(SCENARIOS[name],1):
            event,disk=(read(root,f'{i:02}{suffix}') for suffix in ('','.disk'))
            assert (event['scenario'],event['step'],event['occurrence'],disk['step'])==(name,step,i,step)
            assert event['ms']>last
            last=event['ms']
            data=event['data']
            if step=='bound':
                assert data['url']=='mygo://localhost/' and 'AppleWebKit/' in data['userAgent'] and data['windowID']
                assert data['profile'].endswith('/fixture.noindex')
            buttons=event['view']['buttons']
            assert all(r['step'] not in ('error','unhandled') for r in data['events'])
            if step.endswith('-cancel'):
                assert not event['view']['open'] and clicked(data,'Cancel')
            if step in APPROVALS:
                method,target,label=APPROVALS[step]
                target=disk['disk']['entry'] if target=='$staged' else target
                assert disk['intent']['approval']=={'method':method,'target':target}
                assert event['view']['open'] and any(r['text']==label and not r['disabled'] and not r['hidden'] for r in buttons)
            if step in LOSS_WARNINGS:
                selector,text=LOSS_WARNINGS[step]
                assert event['lossWarning']=={'selector':selector,'text':text,'visible':True}
            if step in ('recovery-before-commit','cold-before-commit'):
                assert event['view']['focus']=='Cancel'
                assert clicked(data,'Force Delete…' if step=='recovery-before-commit' else 'Review remaining files…')
                assert data['target']==disk['intent']['approval']['target']
            if step in ('recovery-before-commit','cold-before-commit'):
                assert data['gitLossWarning']==GIT_LOSS
            commits=[r for r in data['rpc'] if r['method'] in {v[0] for v in APPROVALS.values()}]
            before=step.replace('-after-','-before-') if step.endswith('-after-commit') else 'recovery-before-commit' if step=='recovery-interrupted' else None
            if before:
                method,_,label=APPROVALS[before]
                assert len(commits)==1 and commits[0]['method']==method
                assert commits[0]['ok'] is (step!='recovery-interrupted')
                assert clicked(data,label)
                if step=='recovery-interrupted':
                    assert 'Deletion is incomplete at' in commits[0]['error']
            else:
                assert not commits
            retained=disk['retainedPayloadSHA256']
            assert all(start['retainedPayloadSHA256'][k]==v for k,v in retained.items())
            bound[step]=(event,disk)
        final=bound['complete'][1]['disk']
        if name=='cleanup':
            validate_cleanup(root,bound,start['disk'])
        if name=='ordinary':
            assert initial_ordinary(start['disk'], final)
        if name=='seven':
            assert final=={'phase':'after','workspace_absent':True,'repositories':7,'module_worktrees':112,'outside_controls':3,'all_moved_bytes_identical':True,'checked_before_after_repair':True}
        if episode=='deletion-only':
            diagnostic=read(root,'deletion-diagnostics')
            assert (diagnostic['episode'],diagnostic['scenario'],diagnostic['primaryErrorType'],diagnostic['teardownErrors'])==(episode,name,None,[])
            assert diagnostic['enteredCheckpoint']==len(SCENARIOS[name]) and isinstance(diagnostic['diskSnapshot'],dict)
            assert diagnostic['terminalDisk']=={'verified':True,'disk':final,
                                                'retainedPayloadSHA256':bound['complete'][1]['retainedPayloadSHA256']}
        validated[name]={'pid':pid,'checkpoints':list(bound),'disk':final}
        if name.startswith('recovery-'):
            recovery[name]=bound
    if episode=='deletion-only':
        validate_recovery(folder,recovery)
    return validated


def clicked(data,label):
    return any(e['step']=='button-click' and e['data']['label']==label for e in data['events'])


def initial_ordinary(start,final):
    return not start['ordinaryDeleted'] and final['ordinaryDeleted'] and not final['forceDeleted'] and final['protectedLinksAndBothExternalGitRepositories']=='verified' and final['protectedHeadFileSHA256']==start['protectedHeadFileSHA256']


def validate_cleanup(root,bound,initial):
    assert initial=={'total':32,'removed':[],'protected':['keep-primary','latency-01','latency-29'],'original_refs_preserved':True}
    for index,(step,(event,disk)) in enumerate(bound.items(),1):
        removed=sorted(BULK+['safe-review']) if index>=11 else ['safe-review'] if index>=9 else []
        assert disk['disk']=={**initial,'removed':removed}
        expected_payload=set(read(root,'initial-disk')['retainedPayloadSHA256'])-set(removed)
        assert set(disk['retainedPayloadSHA256'])==expected_payload
    assert clicked(bound['slow-retry-cancel'][0]['data'],'Try again')
    assert re.search(LABELS['timeout'],bound['slow-diagnostic'][0]['view']['text'],re.I)
    assert any(re.search(LABELS['openPR'],r['view'].get('text') or '',re.I) for r in bound['open-pr-kept'][0]['data']['events'])
    event,approved=bound['bulk-before-commit']
    preview,=[r['preview'] for r in event['data']['rpc'] if r.get('ok') is True and r.get('preview',{}).get('target')=='heading']
    rows=preview['rows']
    assert (preview['partial'],preview['retryable'],preview['tokenPresent'],preview['invalid'])==(True,True,True,False)
    assert len(rows)==len({r['dir'] for r in rows})==31
    offered=[r for r in rows if not r['reason']]
    assert {r['dir'] for r in offered}==BULK_OFFER
    seed=read(root,'seed')['checkoutIdentities']
    assert len(seed)==32
    assert offered and all(seed[r['dir']]=={k:r[k] for k in ('instance','head')} for r in offered)
    assert len({r['instance'] for r in offered})==28 and len({r['head'] for r in offered})==1
    assert all(re.fullmatch('[0-9a-f]{32}',r['instance']) and re.fullmatch('[0-9a-f]{40}',r['head']) for r in offered)
    for dir_,label in [('keep-primary','primary'),('latency-01','openPR'),('latency-29','timeout')]:
        assert re.search(LABELS[label],next(r['reason'] for r in rows if r['dir']==dir_),re.I)
    late=approved['intent']['lateUse']
    assert late['cwd'].endswith('/latency-02') and 'p'+str(late['pid']) in late['lsof'] and 'n'+late['cwd'] in late['lsof']
    single=bound['review-after-commit'][0]['data']
    validate_commit_rows(bound,'review-before-commit','review-after-commit',{'safe-review'},single)
    bulk=bound['bulk-after-commit'][0]['data']
    result=validate_commit_rows(bound,'bulk-before-commit','bulk-after-commit',BULK_OFFER,bulk)
    late_summary={'removed':False,**bulk['late']}
    assert bulk['removed']==BULK and late_summary==result['latency-02']
    assert re.search(LABELS['inUse'],result['latency-02']['reason'],re.I)
    spans=[row['data'] for event,_ in bound.values() for row in event['data']['events'] if row['step']=='terminal-preview']
    limits={'latency-29':6500,'latency-01':4500,'safe-review':4500,'heading':16000}
    assert {r['target'] for r in spans}==limits.keys()
    assert all(0<=r['latencyMs']<=r['budgetMs']==limits[r['target']] for r in spans)
    diagnostics=read(root,'diagnostics')
    assert not diagnostics['teardownErrors'] and diagnostics['childTeardown']['joined'] and diagnostics['childTeardown']['pid']==late['pid']
    terminal=diagnostics['terminalDisk']
    assert terminal['verified'] and terminal['disk']==bound['complete'][1]['disk']
    assert terminal['retainedPayloadSHA256']==bound['complete'][1]['retainedPayloadSHA256']
    journal=diagnostics['gh']
    assert journal['status']=='recorded' and not journal['invalidRows'] and not journal['oversized']
    assert any(r['branch']=='fixture:latency-29' and r['phase']=='start' and r['delaySeconds']==30 for r in journal['records'])
    assert any(r['branch']=='fixture:latency-01' and r['phase']=='response-written' and r['delaySeconds']==.2 for r in journal['records'])


def is_sha256(value):
    return isinstance(value,str) and re.fullmatch(r'[0-9a-f]{64}',value) is not None


def validate_commit_rows(bound, before, after, approved, summary):
    rpc, = [r for r in bound[after][0]['data']['rpc'] if r['method']=='ConfirmWorktreeCleanup']
    rows={}
    for row in rpc['rows']:
        path = row['dir']
        removed = row.get('removed', False)  # Go omits the false value.
        reason = row.get('reason', '')
        assert path in approved and path not in rows and type(removed) is bool and isinstance(reason,str)
        assert bool(reason) is not removed
        rows[path] = {'dir':path,'reason':reason,'removed':removed}
    assert rows.keys()==approved
    prior = bound[before][1]
    final = bound[after][1]
    removed = sorted(path for path,row in rows.items() if row['removed'])
    assert summary['removed']==removed
    assert set(final['disk']['removed'])==set(prior['disk']['removed']) | set(removed)
    assert all(path in prior['retainedPayloadSHA256'] and (path not in final['retainedPayloadSHA256']) is row['removed']
               for path,row in rows.items())
    return rows


def validate_recovery(folder, bound):
    live, cold = (bound[name] for name in ('recovery-live','recovery-cold'))
    live_root,cold_root = (folder/name for name in ('recovery-live','recovery-cold'))
    baseline=read(live_root,'seed')
    assert (baseline['absorbedSubmodules'],baseline['gitLinks'])==(112,10)
    assert len(baseline['localStores'])==7 and {p if p.endswith('.git') else p+'/.git' for p in baseline['localStores']}==RECOVERY_STORES
    assert len(baseline['directory'])==2 and all(type(n) is int and n>0 for n in baseline['directory'])
    assert set(baseline['outside'])=={'independent','module-origin'}
    for state in baseline['outside'].values():
        assert set(state)=={'head','refsSHA256','objectsSHA256','payloadSHA256'}
        assert re.fullmatch(r'[0-9a-f]{40}',state['head'])
        assert all(is_sha256(state[k]) for k in ('refsSHA256','objectsSHA256','payloadSHA256'))
    assert set(baseline['protected'])=={'plain-delete','bazel-links'} and all(map(is_sha256,baseline['protected'].values()))
    assert all(is_sha256(baseline[k]) for k in ('headSHA256','configSHA256'))
    contents={k:baseline[k] for k in ('outside','protected','configSHA256','headSHA256')}
    live_disk=dict(contents,live=True,flagged=True,directory=baseline['directory'],approvalPresent=False,earlierMarkerRemoved=False)
    assert read(live_root,'initial-disk')['disk']==live_disk
    for step in ('bound','recovery-live-cancel','recovery-before-commit'):
        assert live[step][1]['disk']==live_disk
    interrupted=live['recovery-interrupted'][1]['disk']
    stage=interrupted['entry']
    match=re.fullmatch(r'\.deleting-v1-([0-9a-f]{32})\.yesman-5',stage)
    assert match is not None and is_sha256(interrupted['approvalSHA256'])
    instance=match[1]
    approval='.approved-deletion-v1-'+instance+'.yesman-5'
    assert interrupted==dict(contents,entry=stage,identity=baseline['directory'],approvalFile=approval,
                             approvalStage=stage,approvalSHA256=interrupted['approvalSHA256'],flagged=True,earlierMarkerRemoved=True)
    assert live['complete'][1]['disk']==interrupted
    thawed={**interrupted,'flagged':False}
    assert read(folder,'release-fault')=={'before':interrupted,'after':thawed}
    assert read(cold_root,'seed')==read(cold_root,'initial-disk')['disk']==thawed
    for step in ('bound','recovery-cold-cancel','cold-before-commit'):
        assert cold[step][1]['disk']==thawed
    complete={'stageRemoved':True,'approvalRemoved':True,'approvedStage':thawed,
              'outside':contents['outside'],'protected':contents['protected']}
    assert cold['cold-after-commit'][1]['disk']==cold['complete'][1]['disk']==complete
    revisions = set()
    for scenario,sequence in ((live,('recovery-live-cancel','recovery-before-commit','recovery-interrupted')),
                              (cold,('recovery-cold-cancel','cold-before-commit','cold-after-commit'))):
        is_cold = scenario is cold
        target = stage if is_cold else 'yesman-5'
        canceled, _ = recovery_review(scenario[sequence[0]][0], target, stage)
        event, approved = scenario[sequence[1]]
        review = recovery_path_receipt(event, target, canceled, stage)
        assert approved['intent']['reviewRevision'] == review['revision']
        for row in (canceled, review):
            assert row['revision'] not in revisions, 'Repeated recovery review revision'
            revisions.add(row['revision'])
        rpc,=[r for r in scenario[sequence[2]][0]['data']['rpc'] if r['method']=='ForceDeleteWorkspace']
        assert (rpc.get('target'), rpc.get('revision')) == (review['target'], review['revision'])
        if not is_cold:
            assert f'/yesmen/{stage}.' in rpc['error'] and re.search(r'removeat config: operation not permitted',rpc['error'],re.I)
    protected={**contents['protected'],'outside/independent':contents['outside']['independent']['payloadSHA256']}
    for root,scenario in ((live_root,live),(cold_root,cold)):
        assert read(root,'initial-disk')['retainedPayloadSHA256']==protected
        assert all(d['retainedPayloadSHA256']==protected for _,d in scenario.values())
    remaining_entries = {'.workspace-deletion-key-v1', *contents['protected']}
    live_snapshot=read(live_root,'deletion-diagnostics')['diskSnapshot']
    assert live_snapshot['entries']==sorted(remaining_entries | {stage,approval})
    assert set(live_snapshot['workspaces'])=={stage} and set(live_snapshot['approvals'])=={approval}
    raw=live_snapshot['workspaces'][stage]
    assert raw['directory']['identity']==baseline['directory'] and raw['earlierMarker']=={'present':False}
    assert raw['config']['flagged'] is True and raw['config']['sha256']==baseline['configSHA256']
    assert raw['HEAD']['sha256']==baseline['headSHA256'] and live_snapshot['approvals'][approval]['sha256']==interrupted['approvalSHA256']
    controls={k:{'observed':contents[k]} for k in ('outside','protected')}
    assert all(live_snapshot[k]==value for k,value in controls.items())
    assert read(cold_root,'deletion-diagnostics')['diskSnapshot']=={'entries':sorted(remaining_entries), 'workspaces':{},'approvals':{},**controls}


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('evidence',type=Path)
    parser.add_argument('contract',type=Path)
    parser.add_argument('run_id')
    parser.add_argument('qa_head')
    parser.add_argument('episode',choices=('cleanup-only','deletion-only'))
    a=parser.parse_args()
    contract_bytes=a.contract.read_bytes()
    print(json.dumps(validate(a.evidence,json.loads(contract_bytes),a.run_id,a.qa_head,a.episode,sha256(contract_bytes).hexdigest()),indent=2))


if __name__=='__main__':
    main()

"""Credential-free native entry; transfer checks precede fixture effects."""
import hashlib,json,os,subprocess,sys
from pathlib import Path
from verify import verify
from provenance import first_attempt
import compatibility_reuse

def main():
    first_attempt()
    if len(sys.argv) == 3 and sys.argv[2] == '--reuse-r5':
        from run_consumer_native import run
        return run(sys.argv[1])
    repo=Path(__file__).resolve().parents[2];verify(repo)
    if any(os.environ.get(k)for k in('GH_TOKEN','GITHUB_TOKEN','MYGO_UPDATER_PRIVATE_KEY')):raise ValueError('native credentials refused')
    root=Path(sys.argv[1]).resolve();rows=json.loads((root/'transfer-files.json').read_bytes())
    for name,digest in rows.items():
        path=root/name
        if path.is_symlink()or not path.is_file()or hashlib.sha256(path.read_bytes()).hexdigest()!=digest:raise ValueError('bound transfer bytes changed: '+name)
    handoff=json.loads((root/'inputs/receipts/hosted-handoff.json').read_bytes())
    if handoff['workflowCommit']!=os.environ['GITHUB_SHA']or handoff['runID']!=os.environ['GITHUB_RUN_ID']or handoff['runAttempt']!=int(os.environ['GITHUB_RUN_ATTEMPT'])or handoff['dispatchApprovedQACommit']!=os.environ['APPROVED_QA_COMMIT']:raise ValueError('specific native authority differs')
    verifier=root/'inputs/public-upgrade-verifier'
    if verifier.is_symlink()or hashlib.sha256(verifier.read_bytes()).hexdigest()!=handoff['publicVerifierSHA256']:raise ValueError('transferred public verifier differs')
    reused=compatibility_reuse.verify(repo,root/'inputs')
    reused['newExecution']={'workflowCommit':os.environ['GITHUB_SHA'],'runID':os.environ['GITHUB_RUN_ID'],'runAttempt':int(os.environ['GITHUB_RUN_ATTEMPT'])}
    verifier.chmod(0o700)  # transfer ZIP drops execute mode; bytes were checked above
    output=Path(os.environ['RUNNER_TEMP'])/'restart-evidence';output.mkdir(exist_ok=False)
    (output/'compatibility-reused.json').write_text(json.dumps(reused,indent=2)+'\n')
    command=[sys.executable,str(repo/'scripts/check-production-upgrade.py')]
    # The immutable, independently accepted .48→S edge is reused as evidence.
    # Only missing stateful proof runs here, using this run's new Q/cache/binding.
    # An error still short-circuits; no failed/unknown mutation is resent.
    for plan,module,evidence in [('plan.json','runner-attachment-f48/runner_attachment.py','stateful')]:
        argv=command+[plan,str(output/evidence),'--t199-attachment',str(repo/'qa/restart-resume'/module),'contract.json']
        result=subprocess.run(argv,cwd=root/'inputs',env=dict(os.environ))
        if result.returncode:raise SystemExit(result.returncode)
if __name__=='__main__':main()

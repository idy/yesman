"""Verify the QA successor and immutable dependency packets before importing."""
import hashlib,json,sys
from pathlib import Path

def verify(root):
    root=Path(root).resolve();rows=json.loads((root/'qa/restart-resume/successor-manifest.json').read_bytes())
    for name,digest in rows.items():
        path=root/name
        if path.is_symlink()or not path.is_file()or hashlib.sha256(path.read_bytes()).hexdigest()!=digest:raise ValueError('QA successor payload differs: '+name)
    prior={'candidate-adapter-f48-repair':'3395b1998e1808ecd7c9d9934064f999d4045de1b46f79a4a4363ade6673561a','candidate-adapter-main048':'b3ad4d7d677f1cbe9c8fed391d92735ba43b0ff2874154ea0a47fc171575c3da','qa-fixture-prep':'4f36fd03d62ca6b48e1fd198e238d6b1866f33737cebc415c51201f38d3deb24'}
    for folder,digest in prior.items():
        base=root/'qa/restart-resume'/folder;raw=(base/'manifest.json').read_bytes()
        if hashlib.sha256(raw).hexdigest()!=digest:raise ValueError('prior seal changed')
        for name,row in json.loads(raw).items():
            b=(base/name).read_bytes()
            if len(b)!=row['bytes']or hashlib.sha256(b).hexdigest()!=row['sha256']:raise ValueError('prior dependency payload changed')
    return rows
if __name__=='__main__':print('PASS:',len(verify(sys.argv[1])),'QA successor inputs and all three immutable dependencies')

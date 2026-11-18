"""Binding-only view of the sealed t199 adapter for the F48 repair.

No launch/profile/native operation runs on import. All implementation functions
come from the verified original packet; only candidate identity pins change.
"""
from pathlib import Path
import hashlib
import importlib.util
import json

CANDIDATE = 'cfaaeec242a7d372669b5e39739b71bbd15ad646'
TREE = 'c7b0c06488b613c56dd0e52ceba98e343a4e0369'
OLD_CANDIDATE = '8a73d96335dc5d2fa787f9b15e1b812bd58b0728'
OLD_TREE = '395ecf86348f0fa5923af118e886efc7c31c39f7'
ORIGINAL_MANIFEST_SHA256 = 'b3ad4d7d677f1cbe9c8fed391d92735ba43b0ff2874154ea0a47fc171575c3da'


def _load():
    folder = Path(__file__).resolve().parent
    contract = json.loads((folder / 'candidate-contract.json').read_bytes())
    if (contract['candidate']['head'], contract['candidate']['tree']) != (CANDIDATE, TREE):
        raise ValueError('corrected contract identity changed')
    original = folder.parent / 'candidate-adapter-main048'
    raw = (original / 'manifest.json').read_bytes()
    if hashlib.sha256(raw).hexdigest() != ORIGINAL_MANIFEST_SHA256:
        raise ValueError('original adapter manifest changed')
    record = json.loads(raw)['adapter.py']
    path = original / 'adapter.py'
    source = path.read_bytes()
    if len(source) != record['bytes'] or hashlib.sha256(source).hexdigest() != record['sha256']:
        raise ValueError('original adapter implementation changed')
    spec = importlib.util.spec_from_file_location('t199_f48_original_adapter', path)
    module = importlib.util.module_from_spec(spec)
    exec(compile(source, str(path), 'exec'), module.__dict__)
    if (module.CANDIDATE, module.TREE) != (OLD_CANDIDATE, OLD_TREE):
        raise ValueError('unexpected original adapter identity')
    module.CANDIDATE, module.TREE = CANDIDATE, TREE
    return module


_impl = _load()
PREDECESSOR = _impl.PREDECESSOR


def __getattr__(name):
    if name.startswith('_'):
        raise AttributeError(name)
    return getattr(_impl, name)

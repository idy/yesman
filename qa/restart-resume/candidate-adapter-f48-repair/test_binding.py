"""Only F48 candidate-rebinding contracts; original seven groups are reused."""
import copy
import json
from pathlib import Path
import unittest
import adapter

HERE = Path(__file__).resolve().parent


def specimen():
    observation = {'about': {'version': 'synthetic-private-Q', 'piManaged': True, 'piState': 'ready'},
                   'state': {'home': '/synthetic/data'}}
    native = {'pid': 10, 'mainSHA256': '4' * 64, 'instanceCount': 1}
    stage = {'version': 'synthetic-private-Q', 'mainSHA256': '4' * 64,
             'candidate': adapter.CANDIDATE, 'candidateTree': adapter.TREE,
             'verificationReceipt': 'synthetic-control-only', 'official': False,
             'privateQA': True, 'sourceFilesVerified': True, 'sourceCommit': 'c' * 40}
    return observation, native, stage


class BindingContracts(unittest.TestCase):
    def test_contract_changes_only_purpose_candidate_and_rebase_receipt(self):
        old = json.loads((HERE.parent / 'candidate-adapter-main048/candidate-contract.json').read_bytes())
        new = json.loads((HERE / 'candidate-contract.json').read_bytes())
        self.assertEqual({k for k in old.keys() | new.keys() if old.get(k) != new.get(k)},
                         {'purpose', 'candidate', 'rebasedFrom'})
        self.assertEqual(new['artifactInputs'], {'S': None, 'Q': None})
        self.assertIsNone(new['managedPi']['artifactRuntimeReceipt'])
        self.assertFalse(new['nativeAcceptance']); self.assertFalse(new['releaseVersionReserved'])
        self.assertEqual(new['candidate']['parent'], old['candidate']['head'])
        self.assertEqual(new['candidate']['base'], old['candidate']['base'])
        self.assertIn('pending independent t206', new['candidate']['sourceAndCurrentGateAcceptance'])

    def test_verified_original_bridge_uses_corrected_pins(self):
        observation, native, stage = specimen()
        result = adapter.bind_bridge(observation, native, '/synthetic/data', stage)
        self.assertEqual(result['sourceProvenance'], 'c' * 40)
        self.assertIsNone(result['observedRevision'])
        self.assertEqual(adapter.bind_bridge.__globals__['CANDIDATE'], adapter.CANDIDATE)
        self.assertEqual(adapter.bind_bridge.__globals__['TREE'], adapter.TREE)
        self.assertEqual(Path(adapter.bind_bridge.__code__.co_filename),
                         HERE.parent / 'candidate-adapter-main048/adapter.py')

    def test_rejected_candidate_and_mixed_identity_cannot_bind(self):
        observation, native, stage = specimen()
        for head, tree in [(adapter.OLD_CANDIDATE, adapter.OLD_TREE),
                           (adapter.OLD_CANDIDATE, adapter.TREE),
                           (adapter.CANDIDATE, adapter.OLD_TREE)]:
            wrong = copy.deepcopy(stage); wrong.update(candidate=head, candidateTree=tree)
            with self.subTest(head=head, tree=tree), self.assertRaisesRegex(ValueError, 'reviewed candidate'):
                adapter.bind_bridge(observation, native, '/synthetic/data', wrong)


if __name__ == '__main__':
    unittest.main(verbosity=2)

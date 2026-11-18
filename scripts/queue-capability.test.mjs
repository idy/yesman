import test from 'node:test';
import assert from 'node:assert/strict';
import {supportsRecall,supportsPromotion} from '../web/recall.js';

test('queue capability is negotiated independently for recall and promotion',()=>{
 assert.equal(supportsRecall(1),true);assert.equal(supportsPromotion(1),false);
 for(const version of [2,3,4]){assert.equal(supportsRecall(version),true);assert.equal(supportsPromotion(version),true)}
 for(const invalid of [undefined,null,false,true,0,-1,5,99,2.5,'1','2','3','4',{},[2],NaN,Infinity]){
  assert.equal(supportsRecall(invalid),false);assert.equal(supportsPromotion(invalid),false);
 }
});

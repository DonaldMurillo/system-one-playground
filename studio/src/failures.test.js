import {test} from 'node:test'
import assert from 'node:assert/strict'
import {actionFailureContracts, failureOutput} from './failures.js'

test('failureOutput preserves custom payload and renders every frame', () => {
  const text = failureOutput({kind:'InvalidCity',message:'city rejected',city:'Atlantis',retryable:false,frames:[
    {action:'lookup',path:'lib.sos',line:8},
    {action:'main',path:'app.sos',line:21,note:'propagated'}
  ]})
  assert.match(text, /^InvalidCity: city rejected/)
  assert.match(text, /"city": "Atlantis"/)
  assert.match(text, /1\. lookup \(lib\.sos:8\)/)
  assert.match(text, /2\. main \(app\.sos:21\)/)
  assert.match(text, /"note": "propagated"/)
})

test('actionFailureContracts exposes only actions with possible failures', () => {
  assert.deepEqual(actionFailureContracts([
    {name:'safe'},
    {name:'load',possibleFailures:['NotFound','Denied']}
  ]), [{name:'load',failures:['NotFound','Denied']}])
})

import assert from 'node:assert/strict'
import test from 'node:test'
import { requestTimingBreakdown } from '../../web/utils/requestTiming.ts'

test('pending characterization does not display a completed zero duration', () => {
  const timing = requestTimingBreakdown({ wait_ms: 40, latency_ms: 1000, characterization_json: JSON.stringify({ classifier_status: 'pending', classification_duration_ms: 0, classification_background: true }) })
  assert.equal(timing.characterizationMS, null)
  assert.equal(timing.totalMS, 1040)
})

test('background classification duration does not inflate provider response time', () => {
  const timing = requestTimingBreakdown({ wait_ms: 40, latency_ms: 1000, characterization_json: JSON.stringify({ classifier_status: 'complete', classification_duration_ms: 4400, classification_background: true }) })
  assert.equal(timing.characterizationMS, 4400)
  assert.equal(timing.totalMS, 1040)
})

test('blocking routing classification still counts toward total time', () => {
  const timing = requestTimingBreakdown({ wait_ms: 40, latency_ms: 1000, characterization_json: JSON.stringify({ classifier_status: 'complete', classification_duration_ms: 4400 }) })
  assert.equal(timing.totalMS, 5440)
})

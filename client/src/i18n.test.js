import { test } from 'node:test'
import assert from 'node:assert/strict'
import { browserLocale } from './i18n.js'

test('browser language picks the page language', () => {
  assert.equal(browserLocale(['zh-CN', 'en']), 'zh')
  assert.equal(browserLocale(['zh-TW']), 'zh')
  assert.equal(browserLocale(['sv-SE', 'en-US']), 'sv')
  assert.equal(browserLocale(['de-DE', 'en']), 'en')    // first language we have wins
  assert.equal(browserLocale(['nb-NO', 'sv']), 'sv')
  assert.equal(browserLocale(['fr-FR']), 'en')          // anything else: English
  assert.equal(browserLocale([]), 'en')
})

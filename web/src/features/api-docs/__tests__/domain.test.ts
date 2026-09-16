import { describe, expect, it } from 'vitest'
import { normalizeDocsURL, renderDoc } from '../api'

describe('documentation domain', () => {
  it('substitutes every request example while retaining media URLs and endpoint paths', () => {
    const original = { id: 'video', title: 'Video', category: 'Video', description: '{{BASE_URL}}', content: 'POST {{BASE_URL}}/v1/videos\nGET {{BASE_URL}}/v1/videos/task\nhttps://example.com/image.png' }
    const result = renderDoc(original, 'https://branch.example.com')
    expect(result.content).toBe('POST https://branch.example.com/v1/videos\nGET https://branch.example.com/v1/videos/task\nhttps://example.com/image.png')
    expect(result.description).toBe('https://branch.example.com')
    expect(original.content).toContain('{{BASE_URL}}')
    expect(renderDoc(original, 'http://another.example:8080').content).toContain('http://another.example:8080/v1/videos')
  })
  it('normalizes empty, trailing slash and port configuration', () => {
    expect(normalizeDocsURL('')).toBe('')
    expect(normalizeDocsURL(' https://branch.example/ ')).toBe('https://branch.example')
    expect(normalizeDocsURL('http://branch.example:8080')).toBe('http://branch.example:8080')
  })
  it.each(['javascript:alert(1)', 'https://user:secret@host', 'https://host/v1', 'https://host?key=secret', 'https://host#fragment', '//host'])('rejects unsafe or non-origin configuration %s', value => {
    expect(() => normalizeDocsURL(value)).toThrow()
  })
})

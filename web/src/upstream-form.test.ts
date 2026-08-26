import { describe, expect, it } from 'vitest'
import {
  COMMON_UPSTREAM_MODELS, DEFAULT_UPSTREAM_PROTOCOLS, UPSTREAM_PROTOCOLS, bulkSetModels, connectionTestText, modelBatchSelection, modelsForPayload, parseModelList, setModelSelected, usesNewAPICredentials,
} from './upstream-form'

describe('upstream form', () => {
  it('新建上游默认使用 Responses 协议', () => {
    expect(DEFAULT_UPSTREAM_PROTOCOLS).toEqual(['responses'])
    expect(UPSTREAM_PROTOCOLS).toEqual(['responses', 'chat', 'messages'])
  })

  it('只为 NewAPI 显示余额凭据', () => {
    expect(usesNewAPICredentials('newapi')).toBe(true)
    expect(usesNewAPICredentials('sub2api')).toBe(false)
  })

  it('整理模型并支持快捷选择', () => {
    expect(parseModelList(' gpt-5, gpt-5, claude-sonnet-4 ')).toEqual(['gpt-5', 'claude-sonnet-4'])
    expect(setModelSelected('gpt-5, claude-sonnet-4', 'gpt-5', false)).toBe('claude-sonnet-4')
    expect(setModelSelected('', 'gpt-5', true)).toBe('gpt-5')
    expect(modelsForPayload('已自动发现的模型', false)).toEqual([])
  })

  it('提供 GPT 5.6 与 Opus 4 常用模型', () => {
    expect(COMMON_UPSTREAM_MODELS).toContain('gpt-5.6')
    expect(COMMON_UPSTREAM_MODELS).toContain('gpt-5.6-codex')
    expect(COMMON_UPSTREAM_MODELS).toContain('claude-opus-4-6')
  })

  it('发现模型的全选和取消不改动手工模型', () => {
    const discovered = ['gpt-5.6', 'claude-opus-4-6', 'gpt-5.6']
    expect(bulkSetModels('manual-model, gpt-5.6', discovered, true))
      .toBe('manual-model, gpt-5.6, claude-opus-4-6')
    expect(bulkSetModels('manual-model, gpt-5.6, claude-opus-4-6', discovered, false))
      .toBe('manual-model')
  })

  it('批量测试只取已发现模型并限制为 20 个', () => {
    const discovered = Array.from({ length: 21 }, (_, index) => `model-${index + 1}`)
    const selection = modelBatchSelection(`manual-model, ${discovered.join(', ')}`, discovered)
    expect(selection).toEqual({ models: discovered.slice(0, 20), total: 21, exceedsLimit: true })
  })

  it('格式化连通性结果', () => {
    expect(connectionTestText({ status: 'healthy', status_code: 200, latency: 12_400_000 })).toBe('连接正常 · HTTP 200 · 12 ms')
    expect(connectionTestText({ status: 'unhealthy', error: 'HTTP 401' })).toBe('HTTP 401')
  })
})

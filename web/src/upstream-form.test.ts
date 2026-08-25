import { describe, expect, it } from 'vitest'
import {
  DEFAULT_UPSTREAM_PROTOCOLS, connectionTestText, modelsForPayload, parseModelList, setModelSelected, usesNewAPICredentials,
} from './upstream-form'

describe('upstream form', () => {
  it('新建上游默认启用全部能力', () => {
    expect(DEFAULT_UPSTREAM_PROTOCOLS).toEqual(['chat', 'responses', 'messages'])
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

  it('格式化连通性结果', () => {
    expect(connectionTestText({ status: 'healthy', status_code: 200, latency: 12_400_000 })).toBe('连接正常 · HTTP 200 · 12 ms')
    expect(connectionTestText({ status: 'unhealthy', error: 'HTTP 401' })).toBe('HTTP 401')
  })
})

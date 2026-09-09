import { z } from 'zod';
import { readFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import { join } from 'node:path';
import type { Usage } from './contracts';
const number = z.number().finite().nullable();
const responseSchema = z.object({
  version: z.literal(1), generated_at: z.string().datetime({ offset: true }), timezone: z.string().min(1),
  stations: z.array(z.object({
    id: z.string().min(1), name: z.string(), enabled: z.boolean().default(true), priority: z.number().default(100),
    groups: z.array(z.object({ id: z.string(), name: z.string(), enabled: z.boolean() })).default([]),
    upstream_today: z.object({requests:number,input_tokens:number,output_tokens:number,cache_read_tokens:number,cache_write_tokens:number,total_tokens:number,actual_cost:number,timezone:z.string().nullable()}).nullable().optional(),
    balance: z.object({ source:z.string().optional(), status: z.enum(['ok','stale','error','unsupported']), available: number, used: number,
      currency: z.string().nullable(), unlimited: z.boolean(), updated_at: z.string().datetime({ offset: true }).nullable() }),
    today: z.object({ requests: number, input_tokens: number, output_tokens: number, total_tokens: number,
      known_input_tokens: number.optional(), known_output_tokens: number.optional(), known_total_tokens: number.optional(), estimated_cost_usd: number, cost_coverage: z.number().min(0).max(1).nullable() }),
  })),
});
export function normalizeUsage(raw: unknown): Usage {
  const data = responseSchema.parse(raw);
  return { status: 'connected', message: '余额来自最近一次快照；费用为统计估算，计价覆盖率不足 100% 时仅包含部分请求，不等同于实际扣款。',
    timezone: data.timezone, fetchedAt: data.generated_at,
    stations: data.stations.filter(s => s.enabled).sort((a,b) => a.priority-b.priority || a.id.localeCompare(b.id, undefined, {numeric:true})).map(s => ({ balanceSource:s.balance.source ?? "", upstreamToday:s.upstream_today ?? null, knownInput:s.today.known_input_tokens ?? null, knownOutput:s.today.known_output_tokens ?? null, id: s.id, priority: s.priority, groups: s.groups, knownTokens: s.today.known_total_tokens ?? null, inputTokens: s.today.input_tokens, outputTokens: s.today.output_tokens, name: s.name, currency: s.balance.currency ?? '',
      remaining: s.balance.available, used: s.balance.used, unlimited: s.balance.unlimited,
      status: s.balance.status, updatedAt: s.balance.updated_at, todayRequests: s.today.requests,
      todayTokens: s.today.total_tokens, todayCost: s.today.estimated_cost_usd, costCoverage: s.today.cost_coverage })),
  };
}
let retryAt = 0;
export async function readUsage(): Promise<Usage> {
  const empty = { fetchedAt: null, timezone: null, stations: [] };
  let credential = process.env.PASEO_RELAY_DAPI_USAGE_CREDENTIAL;
  if (!credential) {
    try { credential = (await readFile(join(homedir(), '.config/paseo-relay-usage/credential'), 'utf8')).trim(); }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') return { ...empty, status: 'error', message: '无法读取只读查询凭据文件，请检查文件权限。' };
    }
  }
  if (!credential) return { ...empty, status: 'unconfigured', message: '尚未配置专用只读查询凭据。可以先查看示例。' };
  if (Date.now() < retryAt) return { ...empty, status: 'error', message: `查询过于频繁，请在 ${Math.ceil((retryAt-Date.now())/1000)} 秒后刷新。` };
  try {
    const base = new URL(process.env.PASEO_RELAY_DAPI_URL || 'https://dapi.gaozhiyi.com');
    if (base.username || base.password || (base.protocol !== 'https:' && !(base.protocol === 'http:' && ['localhost','127.0.0.1','[::1]'].includes(base.hostname)))) {
      return { ...empty, status: 'error', message: '连接地址需要 HTTPS 或本机地址。' };
    }
    const response = await fetch(new URL('/api/readonly/usage', base), {
      headers: { Authorization: `Bearer ${credential}`, 'User-Agent': 'Paseo-Usage/1.0' }, cache: 'no-store', redirect: 'error', signal: AbortSignal.timeout(8000),
    });
    if (response.status === 429) {
      const header = response.headers.get('Retry-After');
      const seconds = header && /^\d+$/.test(header) ? Number(header) : header ? (Date.parse(header)-Date.now())/1000 : 60;
      retryAt = Date.now() + Math.max(1, Number.isFinite(seconds) ? seconds : 60)*1000;
      return { ...empty, status: 'error', message: '查询已限流，请稍后刷新。' };
    }
    if (!response.ok) return { ...empty, status: 'error', message:
      response.status === 401 ? '查询凭据无效或已撤销。' : response.status === 403 ? '查询权限被禁用或连接方式不符合要求。' : response.status === 404 ? '只读用量接口尚未部署或地址不正确。' : '中转站暂时无法返回用量，请稍后刷新。' };
    return normalizeUsage(await response.json());
  } catch {
    return { ...empty, status: 'error', message: '用量查询失败：连接超时或数据格式不兼容。' };
  }
}

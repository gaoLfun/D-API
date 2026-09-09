import { defineRpc } from '@getpaseo/plugin';
import { z } from 'zod';
export const usageSchema = z.object({
  status: z.enum(['connected', 'unconfigured', 'error']),
  timezone: z.string().nullable(), message: z.string(), fetchedAt: z.string().nullable(),
  stations: z.array(z.object({
    balanceSource: z.string(), knownInput: z.number().nullable(), knownOutput: z.number().nullable(),
    upstreamToday: z.object({requests:z.number().nullable(),input_tokens:z.number().nullable(),output_tokens:z.number().nullable(),cache_read_tokens:z.number().nullable(),cache_write_tokens:z.number().nullable(),total_tokens:z.number().nullable(),actual_cost:z.number().nullable(),timezone:z.string().nullable()}).nullable(),
    id: z.string(), priority: z.number(), groups: z.array(z.object({ id: z.string(), name: z.string(), enabled: z.boolean() })),
    knownTokens: z.number().nullable(), inputTokens: z.number().nullable(), outputTokens: z.number().nullable(),
    costCoverage: z.number().nullable(), name: z.string(), currency: z.string(), remaining: z.number().nullable(),
    used: z.number().nullable(), unlimited: z.boolean(), status: z.string(),
    updatedAt: z.string().nullable(), todayRequests: z.number().nullable(),
    todayTokens: z.number().nullable(), todayCost: z.number().nullable(),
  })),
});
export type Usage = z.infer<typeof usageSchema>;
export const getUsage = defineRpc({ name: 'relay.usage', input: z.object({}), output: usageSchema });

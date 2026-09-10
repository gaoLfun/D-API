import { expect, it } from 'vitest'
import { clipWindow, reportWindow, usageWindow } from './drilldown'
it('小时下钻保留时区，按 UTC 精确换算跨日边界', () => {
 expect(usageWindow({ hour: '2026-09-10T00:00:00+08:00' })).toEqual({ since: '2026-09-09T16:00:00.000Z', until: '2026-09-09T17:00:00.000Z' })
 expect(usageWindow({ day: '2026-09-10T00:00:00Z' })).toEqual({ since: '2026-09-10T00:00:00.000Z', until: '2026-09-11T00:00:00.000Z' })
})
it('月桶与周桶裁剪到实际报告范围，不带入未统计日期', () => {
 const bounds = reportWindow(7, new Date('2026-09-10T13:00:00Z'))
 expect(bounds).toEqual({ since: '2026-09-04T00:00:00.000Z', until: '2026-09-11T00:00:00.000Z' })
 expect(clipWindow(usageWindow({ day: '2026-09-01' }, 'month')!, bounds)).toEqual(bounds)
 expect(clipWindow(usageWindow({ day: '2026-09-07' }, 'week')!, bounds)).toEqual({ since: '2026-09-07T00:00:00.000Z', until: bounds.until })
 expect(clipWindow(usageWindow({ day: '2026-08-01' }, 'month')!, bounds)).toBeNull()
 expect(usageWindow({ day: 'invalid' })).toBeNull()
})

import type { PluginSurfaceProps } from '@getpaseo/plugin';
import { useRpc } from '@getpaseo/plugin';
import React, { useEffect, useState } from 'react';
import { ScrollView, Text, View, Pressable } from 'react-native';
import { getUsage, type Usage } from './contracts';
const num = (v: number | null | undefined) => v == null ? '—' : v >= 1e6 ? `${(v/1e6).toFixed(2)}M` : v >= 1e3 ? `${(v/1e3).toFixed(1)}K` : v.toLocaleString();
const money = (v: number | null | undefined, currency = '') => v == null ? '—' : `${currency === 'USD' ? '$' : currency === 'CNY' ? '¥' : ''}${v.toFixed(2)}${currency && !['USD','CNY'].includes(currency) ? ` ${currency}` : ''}`;
const timestamp = (v: string | null) => v ? new Date(v).toLocaleString() : '未提供';
export function MainSurface({ theme, layout }: PluginSurfaceProps) {
  const rpc = useRpc(getUsage);
  const [data,setData] = useState<Usage|null>(null);
  const [loading,setLoading] = useState(false);
  const [error,setError] = useState(false);
  const [group,setGroup] = useState('all');
  const [source,setSource] = useState<'upstream'|'gateway'>('upstream');
  const [expanded,setExpanded] = useState<string|null>(null);
  async function refresh(){setLoading(true);setError(false);setData(null);try{setData(await rpc({}));}catch{setError(true);}finally{setLoading(false);}}
  useEffect(()=>{let active=true;setLoading(true);rpc({}).then(v=>{if(active)setData(v);}).catch(()=>{if(active)setError(true);}).finally(()=>{if(active)setLoading(false);});return()=>{active=false;};},[rpc]);
  const groups = [...new Map((data?.stations??[]).flatMap(s=>s.groups.map(g=>[g.id,g] as const))).values()].sort((a,b)=>a.id.localeCompare(b.id,undefined,{numeric:true}));
  const selected = group === 'all' || groups.some(g=>g.id===group) ? group : 'all';
  const rows=(data?.stations??[]).filter(s=>selected==='all'||s.groups.some(g=>g.id===selected));
  const c=theme.colors;
  const muted={color:c.foregroundMuted,fontSize:11};
  const text={color:c.foreground,fontSize:12};
  const colName={flex:1.25,minWidth:0};const col={flex:1,minWidth:0};
  const pill=(active:boolean)=>({paddingHorizontal:10,paddingVertical:7,borderRadius:6,backgroundColor:active?c.accent:c.surface2});
  return <ScrollView style={{flex:1,backgroundColor:c.surface0}} contentContainerStyle={{padding:layout.compact?10:18,gap:10}}>
    <View style={{flexDirection:'row',alignItems:'center',justifyContent:'space-between'}}>
      <Text style={{...text,fontSize:17,fontWeight:'600'}}>中转站用量</Text>
      <Pressable accessibilityRole="button" disabled={loading} onPress={()=>void refresh()} style={pill(false)}><Text style={text}>{loading?'读取中…':'刷新'}</Text></Pressable>
    </View>
    <View style={{flexDirection:'row',gap:5,flexWrap:'wrap'}}>
      {[{id:'all',name:'全部'},...groups].map(g=><Pressable key={g.id} accessibilityRole="button" accessibilityState={{selected:selected===g.id}} onPress={()=>setGroup(g.id)} style={pill(selected===g.id)}><Text style={{...text,color:selected===g.id?c.accentForeground:c.foreground}}>{g.name}</Text></Pressable>)}
    </View>
    <View style={{flexDirection:'row',alignItems:'center',gap:8}}>
      {(['upstream','gateway'] as const).map(s=><Pressable key={s} accessibilityRole="button" accessibilityState={{selected:source===s}} onPress={()=>setSource(s)}><Text style={{...text,color:source===s?c.accent:c.foregroundMuted,fontWeight:source===s?'700':'400'}}>{s==='upstream'?'上游数据':'网关记录'}</Text></Pressable>)}
      <Text style={{...muted,flex:1,textAlign:'right'}}>{source==='upstream'?'今日按各站日界':`今日 ${data?.timezone??'UTC'}`}</Text>
    </View>
    {error||data?.status!=='connected'?<Text style={{...muted,color:c.statusWarning}}>{error?'插件连接失败，请刷新':data?.message??'正在连接…'}</Text>:null}
    <View style={{borderWidth:1,borderColor:c.border,borderRadius:8,overflow:'hidden'}}>
      <View style={{flexDirection:'row',paddingHorizontal:8,paddingVertical:8,backgroundColor:c.surface2,gap:6}}>
        <Text style={{...muted,...colName}}>上游</Text><Text style={{...muted,...col,textAlign:'right'}}>余额</Text><Text style={{...muted,...col,textAlign:'right'}}>输入 / 输出</Text><Text style={{...muted,...col,textAlign:'right'}}>今日费用</Text>
      </View>
      {rows.map(s=>{
        const u=s.upstreamToday;
        const input=source==='upstream'?u?.input_tokens:s.inputTokens??s.knownInput;
        const output=source==='upstream'?u?.output_tokens:s.outputTokens??s.knownOutput;
        const cost=source==='upstream'?u?.actual_cost:s.todayCost;
        const requests=source==='upstream'?u?.requests:s.todayRequests;
        const stale=s.status==='stale';
        return <View key={s.id} style={{borderTopWidth:1,borderColor:c.border}}>
          <Pressable accessibilityRole="button" accessibilityLabel={`${s.name}详情`} onPress={()=>setExpanded(expanded===s.id?null:s.id)} style={{padding:8,backgroundColor:c.surface1}}>
            <View style={{flexDirection:'row',gap:6,alignItems:'center'}}>
              <View style={colName}><Text numberOfLines={1} style={{...text,fontWeight:'600'}}>{s.name}</Text><Text numberOfLines={1} style={{...muted,marginTop:4}}>P{s.priority} · {num(requests)} 次</Text></View>
              <View style={col}><Text numberOfLines={1} style={{...text,textAlign:'right'}}>{s.unlimited?'不限额':money(s.remaining,s.currency)}</Text><Text numberOfLines={1} style={{...muted,textAlign:'right',marginTop:4}}>{s.unlimited?(s.balanceSource==='billing_subscription'?'订阅额度':'密钥额度'):stale?'历史值':s.status==='ok'?s.currency:'未取得'}</Text></View>
              <View style={col}><Text numberOfLines={1} style={{...text,textAlign:'right'}}>{num(input)}{source==='gateway'&&s.inputTokens===null&&input!=null?'*':''}</Text><Text numberOfLines={1} style={{...text,textAlign:'right',marginTop:4}}>{num(output)}{source==='gateway'&&s.outputTokens===null&&output!=null?'*':''}</Text></View>
              <View style={col}><Text numberOfLines={1} style={{...text,textAlign:'right'}}>{money(cost,source==='upstream'?s.currency:'USD')}</Text><Text numberOfLines={1} style={{...muted,textAlign:'right',marginTop:4}}>{source==='upstream'?(u?stale?'上次快照':'上游实报':'未提供'):'估算'}</Text></View>
            </View>
          </Pressable>
          {expanded===s.id&&<View style={{padding:10,gap:5,backgroundColor:c.surface0}}>
            <Text style={muted}>分组：{s.groups.map(g=>g.name).join('、')||'未分组'}</Text>
            <Text style={muted}>余额查询：{s.balanceSource||'来源未标记'}；已用：{money(s.used,s.currency)}（上游接口口径）</Text>
            <Text style={muted}>上游缓存读 / 写：{num(u?.cache_read_tokens)} / {num(u?.cache_write_tokens)}；总 Token：{num(u?.total_tokens)}</Text>
            <Text style={muted}>上游日界：{u?.timezone??'上游未说明时区'}；快照时间：{timestamp(s.updatedAt)}</Text>
            <Text style={muted}>网关输出：{num(s.outputTokens??s.knownOutput)}{s.outputTokens===null?'（已记录，不完整）':''}；计价覆盖率：{s.costCoverage===null?'—':`${(s.costCoverage*100).toFixed(1)}%`}</Text>
          </View>}
        </View>;
      })}
      {data?.status==='connected'&&rows.length===0&&<Text style={{...muted,padding:12}}>暂无启用上游</Text>}
    </View>
    <Text style={muted}>{source==='upstream'?'数据来自上游定时快照；未提供显示 —，输入与缓存口径由各站定义。':'* 表示网关只记录到部分 Token；费用为估算。'} 点行查看详情。</Text>
    <Text style={muted}>分组仅筛选线路；统计为上游全量。刷新读取快照，不触发上游查询。</Text>
  </ScrollView>;
}

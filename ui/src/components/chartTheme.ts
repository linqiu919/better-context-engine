// Shared recharts styling: bare axes (no lines/ticks, 10px labels) and the
// themed tooltip card used by every console chart.
export const chartAxis={tickLine:false,axisLine:false,tick:{fontSize:10}}
export const chartTooltip={
  contentStyle:{background:'var(--surface)',border:'1px solid var(--line)',borderRadius:8,fontSize:11,padding:'6px 10px'},
  itemStyle:{color:'var(--text)'},
  labelStyle:{color:'var(--muted)'},
}

export function MetricStrip({values}:{values:[string,string|number][]}){
  return <div className="metric-strip">
    {values.map(([label,value])=><div key={label}><span>{label}</span><strong>{value}</strong></div>)}
  </div>
}

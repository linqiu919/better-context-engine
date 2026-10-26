import { NumberRoll } from './NumberRoll'

// Values refresh from SSE without user action, so digits roll to show change.
export function MetricStrip({values}:{values:[string,string|number][]}){
  return <div className="metric-strip">
    {values.map(([label,value])=><div key={label}><span>{label}</span><strong><NumberRoll value={value}/></strong></div>)}
  </div>
}

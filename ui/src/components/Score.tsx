export function Score({value}:{value:number}){
  return <div className="score" aria-label={value.toFixed(2)}>
    <div className="score-track"><span style={{width:`${Math.min(100,value/12*100)}%`}}/></div>
    <strong>{value.toFixed(2)}</strong>
  </div>
}

import type { CSSProperties } from 'react'

const REEL=[...'0123456789'].map(d=><span key={d}>{d}</span>)

// NumberRoll shows a formatted value whose digits roll to their new value when
// it changes on its own (SSE refresh, scroll-in) — "数字滚动": a silent swap
// would leave the reader unaware anything moved. Non-digits render as-is and
// slots are keyed from the right, so units and suffixes stay put while the
// digits in front of them roll. The first render is static.
export function NumberRoll({value}:{value:string|number}){
  const text=String(value)
  const chars=[...text]
  return <span className="num-roll">
    <span className="sr-only">{text}</span>
    <span aria-hidden="true">{chars.map((c,i)=>{
      const key=chars.length-i
      return /\d/.test(c)
        ?<span key={key} className="num-slot"><span className="num-reel" style={{'--d':c} as CSSProperties}>{REEL}</span></span>
        :<span key={`c${key}`}>{c}</span>
    })}</span>
  </span>
}

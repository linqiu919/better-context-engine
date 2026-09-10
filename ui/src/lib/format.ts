import type { Language } from '../i18n'

export const compact=(v:number)=>Intl.NumberFormat('en',{notation:'compact',maximumFractionDigits:1}).format(v||0)
export const bytes=(v:number)=>{if(!v)return'0 B';const u=['B','KB','MB','GB'];const i=Math.min(3,Math.floor(Math.log(v)/Math.log(1024)));return`${(v/1024**i).toFixed(i?1:0)} ${u[i]}`}
export const ms=(v:number)=>v>=1000?`${(v/1000).toFixed(2)} s`:`${Math.round(v||0)} ms`
export const short=(v:string)=>v?v.length>10?`${v.slice(0,8)}…`:v:'—'
export const formatDate=(v:string,language:Language)=>v?new Intl.DateTimeFormat(language==='zh'?'zh-CN':'en',{month:'short',day:'2-digit',hour:'2-digit',minute:'2-digit'}).format(new Date(v)):'—'
// clock formats a timestamp as HH:MM for chart axes and tooltips.
export const clock=(v:number,language:Language)=>new Intl.DateTimeFormat(language==='zh'?'zh-CN':'en',{hour:'2-digit',minute:'2-digit'}).format(v)
// maskToken hides the middle 16 characters of the personal token; the copy
// button still writes the full plaintext to the clipboard.
export const maskToken=(v:string)=>{if(v.length<=20)return v;const keep=v.length-16,front=Math.ceil(keep/2);return `${v.slice(0,front)}${'•'.repeat(16)}${v.slice(front+16)}`}

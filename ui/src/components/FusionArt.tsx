// The three retrieval paths converging into one answer — frameless geometric
// diagram of the actual pipeline, reused on the landing hero and login panel.
export function FusionArt({className}:{className?:string}){
  return <svg className={className} viewBox="0 0 240 200" fill="none" aria-hidden="true">
    <path className="fusion-line fusion-lex" d="M4 24 C 96 24, 120 100, 186 100"/>
    <path className="fusion-line fusion-str" d="M4 100 H 186"/>
    <path className="fusion-line fusion-sem" d="M4 176 C 96 176, 120 100, 186 100"/>
    <path className="fusion-line fusion-out" d="M196 100 H 236"/>
    <path className="fusion-flow fusion-lex" d="M4 24 C 96 24, 120 100, 186 100"/>
    <path className="fusion-flow fusion-flow-2 fusion-str" d="M4 100 H 186"/>
    <path className="fusion-flow fusion-flow-3 fusion-sem" d="M4 176 C 96 176, 120 100, 186 100"/>
    <path className="fusion-flow fusion-flow-out fusion-out" d="M196 100 H 236"/>
    <circle className="fusion-ring" cx="191" cy="100" r="7"/>
    <circle className="fusion-node" cx="191" cy="100" r="4.5"/>
  </svg>
}

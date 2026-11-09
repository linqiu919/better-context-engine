import type { AceProject } from '../types'

// IndexState is the one reading of an ACE project's index lifecycle, derived
// from its counts (the API has no status column): uploading (placeholder row,
// no snapshot yet) → empty (snapshot with no chunks) | indexing (vectors
// still being filled) → ready. pct is floored and capped at 99 so a project
// that is not ready never reads "Indexing 100%".
export type IndexState={kind:'uploading'}|{kind:'empty'}|{kind:'indexing';pct:number}|{kind:'ready'}
export function indexState(p:AceProject):IndexState{
  if(!p.snapshot_id)return {kind:'uploading'}
  if(!p.chunk_count)return {kind:'empty'}
  if(p.embedded_count>=p.chunk_count)return {kind:'ready'}
  return {kind:'indexing',pct:Math.min(99,Math.floor(p.embedded_count/p.chunk_count*100))}
}

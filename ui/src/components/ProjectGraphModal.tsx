import { useEffect, useMemo, useRef, useState } from 'react'
import ForceGraph from 'force-graph'
import { ArrowLeft, Maximize2, Minimize2, Minus, Plus, Target, X } from '@geist-ui/icons'
import { api } from '../api'
import type { GraphEdge, GraphNode, ProjectGraph } from '../types'
import { useI18n } from '../i18n'
import { projectLabel } from '../lib/format'

// ProjectGraphModal renders a project's relation graph. The default canvas
// aggregates symbols into files; clicking a file drills into its symbols
// (plus one-hop neighbors). force-graph is lazy-loaded from the projects page.

type CanvasNode = GraphNode & { file?: boolean; symbols?: number; outside?: boolean }
type CanvasLink = GraphEdge
type CanvasGraph = { nodes: CanvasNode[]; edges: CanvasLink[] }

const esc = (s: string) => s.replace(/[&<>"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]!))
const kindGroup = (kind: string): 'behavior'|'type'|'other' =>
  kind==='func'||kind==='method' ? 'behavior'
  : kind==='class'||kind==='type'||kind==='interface'||kind==='impl' ? 'type'
  : 'other'
const shortLabel = (id: string, max=14) => id.length > max ? id.slice(0, max - 1) + '…' : id
const baseName = (path: string) => {
  const i = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  return i < 0 ? path : path.slice(i + 1)
}

function aggregateByFile(graph: ProjectGraph): CanvasGraph {
  const groups = new Map<string, GraphNode[]>()
  for (const n of graph.nodes) {
    const p = n.path || n.id
    const list = groups.get(p)
    if (list) list.push(n)
    else groups.set(p, [n])
  }
  const nodes: CanvasNode[] = []
  for (const [path, syms] of groups) {
    let refs = 0, types = 0, line = syms[0].line
    for (const s of syms) {
      if (s.refs > refs) refs = s.refs
      if (kindGroup(s.kind) === 'type') types++
      if (s.line < line) line = s.line
    }
    nodes.push({id: path, kind: types > 0 ? 'class' : 'func', path, line, refs, file: true, symbols: syms.length})
  }
  const pathOf = new Map<string, string>()
  for (const n of graph.nodes) pathOf.set(n.id, n.path || n.id)
  const weight = new Map<string, number>()
  for (const e of graph.edges) {
    const a = pathOf.get(e.source), b = pathOf.get(e.target)
    if (!a || !b || a === b) continue
    const key = a + '\0' + b
    weight.set(key, (weight.get(key) || 0) + e.weight)
  }
  const edges: CanvasLink[] = []
  for (const [k, w] of weight) {
    const i = k.indexOf('\0')
    edges.push({source: k.slice(0, i), target: k.slice(i + 1), weight: w})
  }
  return {nodes, edges}
}

function symbolsInFile(graph: ProjectGraph, file: string): CanvasGraph {
  const inFile = new Set<string>()
  for (const n of graph.nodes) if (n.path === file) inFile.add(n.id)
  const keep = new Set(inFile)
  for (const e of graph.edges) {
    if (inFile.has(e.source)) keep.add(e.target)
    if (inFile.has(e.target)) keep.add(e.source)
  }
  const nodes: CanvasNode[] = []
  for (const n of graph.nodes) if (keep.has(n.id)) nodes.push({...n, outside: n.path !== file})
  const edges: CanvasLink[] = []
  for (const e of graph.edges) if (keep.has(e.source) && keep.has(e.target)) edges.push(e)
  return {nodes, edges}
}

function GraphLoading() {
  const {t} = useI18n()
  const nodes: [number, number, string][] = [[24,26,'b'],[62,14,'t'],[102,32,'b'],[38,66,'t'],[78,72,'b'],[108,58,'o']]
  const edges: [number, number][] = [[0,1],[1,2],[0,3],[3,4],[4,2],[4,5],[1,4]]
  return <div className="graph-state graph-loading">
    <svg viewBox="0 0 128 88" aria-hidden="true">
      {edges.map(([a,b],i)=><line key={i} className="gl-edge" style={{animationDelay:`${i*0.18}s`}}
        x1={nodes[a][0]} y1={nodes[a][1]} x2={nodes[b][0]} y2={nodes[b][1]}/>)}
      {nodes.map(([x,y,g],i)=><circle key={i} className={`gl-node gl-${g}`} style={{animationDelay:`${i*0.22}s`}} cx={x} cy={y} r="4"/>)}
    </svg>
    <span className="graph-loading-title">{t('Mapping relations')}</span>
    <span className="graph-loading-hint">{t('The first build scans the whole project and can take a few seconds; later opens load from cache.')}</span>
  </div>
}

export default function ProjectGraphModal({project, branch, onClose}: {project: string; branch?: string; onClose: () => void}) {
  const {t} = useI18n()
  const [graph, setGraph] = useState<ProjectGraph|null>(null)
  const [error, setError] = useState('')
  const [full, setFull] = useState(false)
  const [focusFile, setFocusFile] = useState<string|null>(null)
  const canvasRef = useRef<HTMLDivElement>(null)
  const fgRef = useRef<ForceGraph|null>(null)
  const files = useMemo(() => graph ? aggregateByFile(graph) : null, [graph])
  const singleFile = Boolean(files && files.nodes.length <= 1)
  const viewingFile = focusFile || (singleFile && files?.nodes[0] ? files.nodes[0].id : null)
  const drilled = Boolean(viewingFile)
  const canvas = useMemo(() => {
    if (!graph || !files) return null
    return viewingFile ? symbolsInFile(graph, viewingFile) : files
  }, [graph, files, viewingFile])
  const showBack = Boolean(focusFile) && !singleFile

  useEffect(() => {
    let alive = true
    api<ProjectGraph>(`/api/v1/me/ace-projects/${encodeURIComponent(project)}/graph${branch ? `?branch=${encodeURIComponent(branch)}` : ''}`)
      .then(g => {if (alive) setGraph(g)})
      .catch(e => {if (alive) setError((e as Error).message)})
    return () => {alive = false}
  }, [project, branch])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      if (showBack) {setFocusFile(null); return}
      onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose, showBack])

  useEffect(() => {
    const el = canvasRef.current
    if (!canvas || canvas.nodes.length === 0 || !el) return
    const css = getComputedStyle(document.documentElement)
    const cvar = (name: string, fallback: string) => css.getPropertyValue(name).trim() || fallback
    const colors = {
      behavior: cvar('--blue', '#2f6fed'),
      type: cvar('--warn', '#b45309'),
      other: cvar('--muted', '#8a8577'),
      text: cvar('--text', '#1c1a17'),
      line: cvar('--line-strong', '#d8d3c8'),
    }
    const mono = cvar('--font-mono', 'ui-monospace, monospace')
    const fg = new ForceGraph(el)
    fgRef.current = fg
    let fitted = false
    fg.graphData({nodes: canvas.nodes.map(n => ({...n})), links: canvas.edges.map(e => ({...e}))})
      .width(el.clientWidth).height(el.clientHeight)
      .backgroundColor('rgba(0,0,0,0)')
      .nodeLabel((n: any) => n.file
        ? `<div class="graph-tip"><strong>${esc(baseName(n.path))}</strong><span>${esc(n.path)}</span><span>${t('{n} symbols in this file').replace('{n}', String(n.symbols||0))}</span></div>`
        : `<div class="graph-tip"><strong>${esc(n.id)}</strong><span>${esc(n.path)}:${n.line}</span><span>${t('{n} referencing files').replace('{n}', String(n.refs))}</span>${n.summary ? `<em>${esc(n.summary)}</em>` : ''}</div>`)
      .nodeCanvasObject((n: any, ctx, scale) => {
        const r = n.file
          ? Math.min(5 + Math.sqrt(n.symbols || 1) * 1.4, 12)
          : Math.min(3 + Math.sqrt(n.refs || 1) * 1.1, 9)
        ctx.beginPath()
        ctx.arc(n.x, n.y, r, 0, 2 * Math.PI)
        ctx.fillStyle = colors[kindGroup(n.kind)]
        ctx.globalAlpha = n.outside ? 0.4 : 0.92
        ctx.fill()
        ctx.globalAlpha = 1
        if (scale >= 0.7) {
          ctx.font = `${11 / scale}px ${mono}`
          ctx.textAlign = 'center'
          ctx.textBaseline = 'top'
          ctx.fillStyle = colors.text
          ctx.globalAlpha = n.outside ? 0.55 : 1
          ctx.fillText(n.file ? shortLabel(baseName(n.path), 18) : shortLabel(n.id), n.x, n.y + r + 3 / scale)
          ctx.globalAlpha = 1
        }
      })
      .nodePointerAreaPaint((n: any, color, ctx) => {
        const r = (n.file
          ? Math.min(5 + Math.sqrt(n.symbols || 1) * 1.4, 12)
          : Math.min(3 + Math.sqrt(n.refs || 1) * 1.1, 9)) + 4
        ctx.beginPath()
        ctx.arc(n.x, n.y, r, 0, 2 * Math.PI)
        ctx.fillStyle = color
        ctx.fill()
      })
      .linkColor(() => colors.line)
      .linkWidth((l: any) => Math.min(0.5 + l.weight / 8, 2.5))
      .linkDirectionalArrowLength(3)
      .linkDirectionalArrowRelPos(1)
      .cooldownTicks(150)
      .onEngineStop(() => {if (!fitted) {fitted = true; fg.zoomToFit(400, 60)}})
      .onNodeClick((n: any) => {if (n.file) setFocusFile(n.id)})
      .onNodeHover((n: any) => {el.style.cursor = n && n.file ? 'pointer' : 'grab'})
    const ro = new ResizeObserver(() => fg.width(el.clientWidth).height(el.clientHeight))
    ro.observe(el)
    return () => {ro.disconnect(); fg._destructor(); fgRef.current = null}
  }, [canvas, t])

  const zoomBy = (k: number) => {const fg = fgRef.current; if (fg) fg.zoom(fg.zoom() * k, 250)}
  const stats = canvas && canvas.nodes.length > 0
    ? drilled
      ? t('{n} symbols · {m} relations').replace('{n}', String(canvas.nodes.length)).replace('{m}', String(canvas.edges.length))
      : t('{n} files · {m} relations').replace('{n}', String(canvas.nodes.length)).replace('{m}', String(canvas.edges.length))
    : ''
  return <div className="modal-backdrop graph-backdrop" onClick={onClose}>
    <div className={`graph-modal${full ? ' full' : ''}`} role="dialog" aria-modal="true" aria-label={t('Relation graph')} onClick={e => e.stopPropagation()}>
      <header className="graph-head">
        <div className="graph-title">
          <span className="graph-eyebrow">{t('Relation graph')}</span>
          <strong>{showBack && viewingFile ? baseName(viewingFile) : projectLabel(project, branch)}</strong>
          {stats && <span className="graph-stats">{stats}</span>}
        </div>
        <div className="graph-controls">
          {showBack && <button className="graph-btn" onClick={()=>setFocusFile(null)} title={t('Back to files')} aria-label={t('Back to files')}><ArrowLeft size={14}/></button>}
          <button className="graph-btn" onClick={() => zoomBy(1 / 1.5)} title={t('Zoom out')} aria-label={t('Zoom out')}><Minus size={14}/></button>
          <button className="graph-btn" onClick={() => zoomBy(1.5)} title={t('Zoom in')} aria-label={t('Zoom in')}><Plus size={14}/></button>
          <button className="graph-btn" onClick={() => fgRef.current?.zoomToFit(400, 60)} title={t('Fit view')} aria-label={t('Fit view')}><Target size={14}/></button>
          <button className="graph-btn" onClick={() => setFull(f => !f)} title={t(full ? 'Exit fullscreen' : 'Fullscreen')} aria-label={t(full ? 'Exit fullscreen' : 'Fullscreen')}>{full ? <Minimize2 size={14}/> : <Maximize2 size={14}/>}</button>
          <button className="graph-btn" onClick={onClose} title={t('Close')} aria-label={t('Close')}><X size={14}/></button>
        </div>
      </header>
      <div className="graph-body">
        {!graph && !error && <GraphLoading/>}
        {error && <div className="graph-state"><p className="form-error">{t('Failed to build the graph.')} {error}</p></div>}
        {graph && graph.nodes.length === 0 && <div className="graph-state"><p>{t('No core relations detected in this project.')}</p></div>}
        {graph && graph.nodes.length > 0 && canvas && <>
          <div ref={canvasRef} className="graph-canvas"/>
          <div className="graph-legend">
            <span><i className="graph-dot behavior"/>{t('Functions & methods')}</span>
            <span><i className="graph-dot type"/>{t('Classes & types')}</span>
            <span className="graph-legend-hint">{drilled ? t('Drag to pan · scroll to zoom · hover for details') : t('Click a file to inspect its symbols.')}</span>
          </div>
        </>}
      </div>
    </div>
  </div>
}

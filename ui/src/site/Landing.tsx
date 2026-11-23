import { useEffect, useRef, useState } from 'react'
import { Button } from '@geist-ui/core'
import { ArrowDown, ArrowRight, ArrowUp, ArrowUpRight, BookOpen, Clock, Copy, GitBranch, Layers, Share2, Shield, Terminal, Trash2, UploadCloud } from '@geist-ui/icons'
import { BrandLogo } from '../BrandLogo'
import { useI18n } from '../i18n'
import { DOCS_URL } from '../nav'
import { prefersReducedMotion } from '../lib/motion'
import { Slogan } from '../components/Slogan'
import { LangThemeSwitch } from '../components/LangThemeSwitch'
import { ClaudeMark, CodexMark, CursorMark, DeepSeekLink, McpMark, VSCodeMark, ZedMark } from '../components/ToolMarks'
import { NumberRoll } from '../components/NumberRoll'
import { ToolGroup } from '../components/ToolGroup'
import { TextSwap } from '../components/TextSwap'
import { useGlide } from '../hooks/useGlide'
import { useCopy } from '../hooks/useCopy'
import { McpConfigPanel } from './McpConfigPanel'
import { PublicNav, SiteFooter } from './PublicChrome'

// The live-indexing screen rotates through three copy sets — one per ingest
// property (save-time checkpoints / diff-only upload / branch & worktree).
const liveCopySets = [
  { eyebrow: 'Live indexing', title: 'Your code, indexed as you save', lead: 'Every save becomes an incremental checkpoint: code is split into fragments while terms, symbols and vectors update in step — your next search always sees the latest code.' },
  { eyebrow: 'Diff-only upload', title: 'Only changed content travels', lead: 'Fragments are content-addressed. The client asks which blobs the server is missing and uploads only those — unchanged code never crosses the wire twice.' },
  { eyebrow: 'Branches & worktrees', title: 'Every branch keeps its own index', lead: 'Each branch and worktree is tracked as its own project, while the content they share is stored — and counted against your quota — exactly once.' },
]

// Hero console replay: one retrieval request, stage by stage. Offsets and
// widths are percentages of the 5s latency budget track.
const heroStages = [
  { key: 'Recall', note: 'lexical · structural · semantic', from: 0, width: 9, ms: '430 ms' },
  { key: 'Rerank', note: 'cross-encoder top-K', from: 9, width: 13, ms: '650 ms' },
  { key: 'Curate', note: 'merge · expand · relate', from: 22, width: 2, ms: '40 ms' },
]
const heroHits: [string, string, string][] = [
  ['internal/auth/ratelimit.go', 'L18–64', '0.92'],
  ['internal/httpapi/login.go', 'L102–141', '0.86'],
  ['internal/httpapi/redis.go', 'L77–110', '0.71'],
]

const skillSnippet = `# 1 · environment
export BCE_BASE_URL="https://bce.wxnext.top"
export BCE_TOKEN="bce_..."

# 2 · save skills/bce/SKILL.md into your agent's skills folder

# 3 · the agent then searches in one-shot mode
npx -y bce-tool --base-url "$BCE_BASE_URL" \\
  --token "$BCE_TOKEN" --search "where is login rate limiting handled"`

// Numbered screens, in page order — the eyebrows and the closing index share
// this list so the numbering can never drift.
const chapters = [
  { id: 'indexing-screen', title: 'Live indexing', sub: 'Always up to date' },
  { id: 'retrieval-screen', title: 'Retrieval pipeline', sub: 'Recall, rerank, curate, relate' },
  { id: 'broad-screen', title: 'Broad questions', sub: 'Architecture-level answers' },
  { id: 'enhance-screen', title: 'Prompt enhancement', sub: 'From a vague question to a precise prompt' },
  { id: 'change-screen', title: 'Pay only for change', sub: 'Dedup, diff upload, clear quotas' },
  { id: 'start-screen', title: 'Getting started', sub: 'MCP or Agent Skill' },
]
const chapterNo = (id: string) => String(chapters.findIndex(c => c.id === id) + 1).padStart(2, '0')

// Public landing page (signed-out default): hero plus six numbered screens —
// live indexing, retrieval pipeline, broad questions, prompt enhancement,
// pay-for-change and getting started — then the closing CTA and index.
export function Landing({onSignIn,dark,toggleTheme}:{onSignIn:()=>void;dark:boolean;toggleTheme:()=>void}){
  const {t}=useI18n()
  const [scrolled,setScrolled]=useState(false)
  const [showTop,setShowTop]=useState(false)
  const [copySet,setCopySet]=useState(0)
  const [entry,setEntry]=useState<'mcp'|'skill'>('mcp')
  const [skillCopied,copySkill]=useCopy()
  const pageRef=useRef<HTMLDivElement>(null)
  // Copy rotation is motion, so it is disabled under reduced-motion (the
  // first set then stays static); the swap animation itself is CSS-gated.
  useEffect(()=>{if(prefersReducedMotion())return;const id=window.setInterval(()=>setCopySet(i=>(i+1)%liveCopySets.length),5200);return()=>window.clearInterval(id)},[])
  // Choreographed swap: the outgoing set fades up first, then the incoming
  // one enters line by line. The dots move at once; the copy trails them.
  const [shownSet,setShownSet]=useState(0)
  const [copyLeaving,setCopyLeaving]=useState(false)
  useEffect(()=>{
    if(copySet===shownSet)return
    if(prefersReducedMotion()){setShownSet(copySet);return}
    setCopyLeaving(true)
    const id=window.setTimeout(()=>{setShownSet(copySet);setCopyLeaving(false)},200)
    return()=>window.clearTimeout(id)
  },[copySet,shownSet])
  // Screens reveal once, when their top crosses the lower fifth of the
  // viewport (not a ratio threshold: stacked mobile screens can be several
  // viewports tall): card groups stagger in and the facts roll up from zero.
  const [seen,setSeen]=useState<Record<string,boolean>>({})
  useEffect(()=>{
    const root=pageRef.current
    if(!root)return
    const io=new IntersectionObserver(entries=>{
      const hit=entries.filter(e=>e.isIntersecting).map(e=>e.target.id)
      if(hit.length)setSeen(s=>({...s,...Object.fromEntries(hit.map(id=>[id,true]))}))
      entries.forEach(e=>{if(e.isIntersecting)io.unobserve(e.target)})
    },{root,rootMargin:'0px 0px -20% 0px'})
    root.querySelectorAll('.landing-screen[id]').forEach(el=>io.observe(el))
    return()=>io.disconnect()
  },[])
  const screen=(id:string,extra='')=>({id,className:`landing-screen${extra} ${seen[id]?'in-view':''}`})
  const eyebrow=(id:string,label:string)=><span className="section-eyebrow"><b>{chapterNo(id)}</b>{t(label)}</span>
  const dots=useGlide<HTMLDivElement>('button',[copySet],{hover:false})
  const endCells=useGlide<HTMLDivElement>('.end-cell',[])
  const tools=useGlide<HTMLDivElement>('.tool',[])
  const entryTabs=useGlide<HTMLDivElement>('button',[entry],{hover:false})
  // Anchor scroll stays inside the landing scroll container; location.hash is
  // reserved for login/landing routing, so sections are reached by id.
  const scrollToScreen=(id:string)=>{document.getElementById(id)?.scrollIntoView({behavior:prefersReducedMotion()?'auto':'smooth'})}
  const scrollToTop=()=>{pageRef.current?.scrollTo({top:0,behavior:prefersReducedMotion()?'auto':'smooth'})}
  const roll=(id:string,v:string|number,zero:string|number=0)=><NumberRoll value={seen[id]?v:zero}/>
  return <div className="landing-page" ref={pageRef} onScroll={e=>{const top=e.currentTarget.scrollTop;setScrolled(top>24);setShowTop(top>560)}}>
    <PublicNav scrolled={scrolled} onBrand={scrollToTop} brandLabel={t('Back to top')}>
      <ToolGroup>
        <button className="icon-button" onClick={()=>window.open(DOCS_URL,'_blank','noopener')} aria-label={t('Open documentation')}><BookOpen size={17}/></button>
        <button className="icon-button" onClick={()=>{location.hash='mcp'}} aria-label={t('MCP server page')}><McpMark/></button>
        <button className="icon-button" onClick={()=>{location.hash='skill'}} aria-label={t('Agent Skill page')}><Terminal size={17}/></button>
        <DeepSeekLink label={t('DeepSeek project')}/>
        <LangThemeSwitch dark={dark} toggleTheme={toggleTheme}/>
      </ToolGroup>
      <Button auto scale={0.75} type="secondary" onClick={onSignIn}>{t('Get started')}</Button>
    </PublicNav>
    <main>
      <section className="landing-screen">
        <div className="scroll-cue" aria-hidden="true"><span>{t('Scroll')}</span><i/></div>
        <div className="landing-hero">
          <div className="landing-hero-copy">
            <span className="hero-eyebrow"><i/>{t('MCP · Agent Skill')}</span>
            <Slogan/>
            <p>{t('Three-way recall, reranking and curation hand your coding agent the right code fragments — within a five-second budget.')}</p>
            <div className="landing-cta"><Button auto type="secondary" className="brand-btn" onClick={onSignIn}>{t('Get started')}</Button><button type="button" className="ghost-link" onClick={()=>scrollToScreen('start-screen')}>{t('See how to connect')}<ArrowDown size={13}/></button></div>
          </div>
          <div className="hero-console" aria-hidden="true">
            <div className="win-chrome"><i/><i/><i/><span>codebase-retrieval</span></div>
            <div className="console-query"><span className="console-dot"/><code>{t('"where is login rate limiting handled"')}</code></div>
            <div className="console-stages">
              {heroStages.map((s,i)=><div className="stage-row" key={s.key} style={{'--i':i} as React.CSSProperties}>
                <span className="stage-name">{t(s.key)}<small>{s.note}</small></span>
                <span className="stage-track"><span className="stage-bar" style={{left:`${s.from}%`,width:`${s.width}%`}}/></span>
                <span className="stage-ms">{s.ms}</span>
              </div>)}
              <div className="stage-total"><span>{t('Total')}</span><strong>1.12 s</strong><small>/ 5 s {t('budget')}</small></div>
            </div>
            <div className="console-hits">
              {heroHits.map(([p,l,s],i)=><div className="console-hit" key={p} style={{'--i':i} as React.CSSProperties}><code>{p}</code><span className="hit-lines">{l}</span><span className="hit-score">{s}</span></div>)}
              <div className="console-related"><span>related_symbols</span><code>loginLimiter.Allow · RetryAfter</code></div>
            </div>
          </div>
        </div>
      </section>

      <section {...screen('indexing-screen')}>
        <div className="landing-live">
          <div className="index-panel" aria-hidden="true">
            <div className="win-chrome"><i/><i/><i/><span>{t('Live indexing')}</span></div>
            <svg viewBox="0 0 560 300" className="index-scene">
              <path className="ilx-wire" d="M300 71 C330 71 350 70 380 70"/>
              <path className="ilx-wire" d="M300 159 C330 159 350 150 380 150"/>
              <path className="ilx-wire" d="M300 247 C330 247 350 230 380 230"/>
              <rect className="ilx-chunk lex" x="24" y="32" width="268" height="78" rx="8"/>
              <rect className="ilx-chunk str" x="24" y="120" width="268" height="78" rx="8"/>
              <rect className="ilx-chunk sem" x="24" y="208" width="268" height="78" rx="8"/>
              <g className="ilx-code">
                <rect x="40" y="44" width="168" height="8" rx="4"/><rect x="40" y="62" width="112" height="8" rx="4"/><rect x="40" y="80" width="196" height="8" rx="4"/><rect x="40" y="98" width="88" height="8" rx="4"/>
                <rect x="40" y="132" width="140" height="8" rx="4"/><rect x="40" y="150" width="208" height="8" rx="4"/><rect x="40" y="168" width="96" height="8" rx="4"/><rect x="40" y="186" width="172" height="8" rx="4"/>
                <rect x="40" y="220" width="124" height="8" rx="4"/><rect x="40" y="238" width="188" height="8" rx="4"/><rect x="40" y="256" width="80" height="8" rx="4"/><rect x="40" y="274" width="152" height="8" rx="4"/>
              </g>
              <rect className="ilx-chip" x="380" y="52" width="164" height="36" rx="8"/>
              <rect className="ilx-chip" x="380" y="132" width="164" height="36" rx="8"/>
              <rect className="ilx-chip" x="380" y="212" width="164" height="36" rx="8"/>
              <text className="ilx-chip-text lex" x="398" y="75">LEXICAL</text>
              <text className="ilx-chip-text str" x="398" y="155">STRUCTURAL</text>
              <text className="ilx-chip-text sem" x="398" y="235">SEMANTIC</text>
              <g className="ilx-anim">
                <rect className="ilx-scan" x="20" y="22" width="276" height="2" rx="1"><animateTransform attributeName="transform" type="translate" values="0 0;0 264" dur="6s" repeatCount="indefinite"/></rect>
                <rect className="ilx-pulse lex" x="24" y="32" width="268" height="78" rx="8" opacity="0"><animate attributeName="opacity" values="0;.14;.14;0;0" keyTimes="0;0.04;0.24;0.28;1" dur="6s" repeatCount="indefinite"/></rect>
                <rect className="ilx-pulse str" x="24" y="120" width="268" height="78" rx="8" opacity="0"><animate attributeName="opacity" values="0;.14;.14;0;0" keyTimes="0;0.36;0.56;0.60;1" dur="6s" repeatCount="indefinite"/></rect>
                <rect className="ilx-pulse sem" x="24" y="208" width="268" height="78" rx="8" opacity="0"><animate attributeName="opacity" values="0;.14;.14;0;0" keyTimes="0;0.68;0.88;0.92;1" dur="6s" repeatCount="indefinite"/></rect>
                <circle className="ilx-dot lex" r="4.5" opacity="0">
                  <animateMotion path="M300 71 C330 71 350 70 380 70" dur="6s" repeatCount="indefinite" calcMode="linear" keyPoints="0;0;1;1" keyTimes="0;0.14;0.28;1"/>
                  <animate attributeName="opacity" values="0;1;1;0;0" keyTimes="0;0.14;0.27;0.30;1" dur="6s" repeatCount="indefinite"/>
                </circle>
                <circle className="ilx-dot str" r="4.5" opacity="0">
                  <animateMotion path="M300 159 C330 159 350 150 380 150" dur="6s" repeatCount="indefinite" calcMode="linear" keyPoints="0;0;1;1" keyTimes="0;0.46;0.60;1"/>
                  <animate attributeName="opacity" values="0;1;1;0;0" keyTimes="0;0.46;0.59;0.62;1" dur="6s" repeatCount="indefinite"/>
                </circle>
                <circle className="ilx-dot sem" r="4.5" opacity="0">
                  <animateMotion path="M300 247 C330 247 350 230 380 230" dur="6s" repeatCount="indefinite" calcMode="linear" keyPoints="0;0;1;1" keyTimes="0;0.78;0.92;1"/>
                  <animate attributeName="opacity" values="0;1;1;0;0" keyTimes="0;0.78;0.91;0.94;1" dur="6s" repeatCount="indefinite"/>
                </circle>
                <rect className="ilx-flash lex" x="380" y="52" width="164" height="36" rx="8" opacity="0"><animate attributeName="opacity" values="0;0;.22;0;0" keyTimes="0;0.28;0.33;0.44;1" dur="6s" repeatCount="indefinite"/></rect>
                <rect className="ilx-flash str" x="380" y="132" width="164" height="36" rx="8" opacity="0"><animate attributeName="opacity" values="0;0;.22;0;0" keyTimes="0;0.60;0.65;0.76;1" dur="6s" repeatCount="indefinite"/></rect>
                <rect className="ilx-flash sem" x="380" y="212" width="164" height="36" rx="8" opacity="0"><animate attributeName="opacity" values="0;0;.22;0;0" keyTimes="0;0.92;0.96;0.995;1" dur="6s" repeatCount="indefinite"/></rect>
              </g>
            </svg>
          </div>
          <div className="live-copy">
            <div className={`live-copy-set ${copyLeaving?'leaving':''}`} key={shownSet}>
              <span className="section-eyebrow"><b>{chapterNo('indexing-screen')}</b>{t(liveCopySets[shownSet].eyebrow)}</span>
              <h2>{t(liveCopySets[shownSet].title)}</h2>
              <p className="section-lead">{t(liveCopySets[shownSet].lead)}</p>
            </div>
            <div className="copy-dots glide" ref={dots.ref}>{dots.layer}{liveCopySets.map((s,i)=><button key={i} type="button" className={i===copySet?'active':''} aria-label={t('Show copy set {n}',{n:i+1})} onClick={()=>setCopySet(i)}/>)}</div>
          </div>
        </div>
      </section>

      <section {...screen('retrieval-screen')}>
        <div className="landing-features">
          {eyebrow('retrieval-screen','Retrieval pipeline')}
          <h2>{t('Four stages between your question and the answer')}</h2>
          <p className="section-lead">{t('Three recalls run in parallel, a reranker judges every candidate, curation turns the survivors into readable context, and a relation pass points at the code they touch.')}</p>
          <ol className="pipeline">
            <li className="pipe-stage">
              <span className="pipe-node">1</span>
              <h3>{t('Recall')}</h3>
              <p>{t('Lexical BM25, path-and-symbol structure and semantic vectors recall candidates side by side, fused by reciprocal rank.')}</p>
              <div className="pipe-chips"><span className="path-lex">{t('Lexical')}</span><span className="path-str">{t('Structural')}</span><span className="path-sem">{t('Semantic')}</span></div>
            </li>
            <li className="pipe-stage">
              <span className="pipe-node">2</span>
              <h3>{t('Rerank')}</h3>
              <p>{t('A cross-encoder scores every candidate against the question and cuts the list where relevance drops off.')}</p>
            </li>
            <li className="pipe-stage">
              <span className="pipe-node">3</span>
              <h3>{t('Curate')}</h3>
              <p>{t('At most three fragments per file, adjacent fragments merged, tiny ones widened with surrounding lines.')}</p>
            </li>
            <li className="pipe-stage">
              <span className="pipe-node">4</span>
              <h3>{t('Relate')}</h3>
              <p>{t('One-hop call-graph expansion and interface-to-implementation bridging, plus grep-ready related symbols.')}</p>
            </li>
          </ol>
          <div className="landing-facts">
            <div className="fact"><strong>{roll('retrieval-screen',3)}</strong><small>{t('Recall paths fused')}</small></div>
            <div className="fact"><strong>{roll('retrieval-screen',8)}</strong><small>{t('Languages with AST chunking')}</small></div>
            <div className="fact"><strong>{roll('retrieval-screen','5s','0s')}</strong><small>{t('End-to-end latency budget')}</small></div>
            <div className="fact"><strong>≤{roll('retrieval-screen',3)}</strong><small>{t('Fragments per file')}</small></div>
          </div>
          <p className="pipeline-note">{t('Over budget, a stage degrades instead of failing — you always get an answer.')}</p>
        </div>
      </section>

      <section {...screen('broad-screen')}>
        <div className="landing-broad">
          <div className="broad-copy">
            {eyebrow('broad-screen','Broad questions')}
            <h2>{t('Ask about the whole system, not just one function')}</h2>
            <p className="section-lead">{t('When no fragment answers a question convincingly, BCE switches to broad mode: the question is split into focused sub-queries, manifests and entry points are weighted up, and the answer comes back as a skeleton of the codebase.')}</p>
            <ul className="broad-points">
              <li><Layers size={15}/><span>{t('Sub-queries recalled and reranked in parallel')}</span></li>
              <li><Share2 size={15}/><span>{t('Manifests, configs and entry points as structural priors')}</span></li>
              <li><BookOpen size={15}/><span>{t('Skeleton excerpts: up to 24 files, two signatures each')}</span></li>
            </ul>
          </div>
          <div className="broad-panel" aria-hidden="true">
            <div className="broad-question"><span className="console-dot"/>{t('"how is this project put together?"')}</div>
            <div className="broad-fan">
              <span>{t('entry point & routing')}</span>
              <span>{t('indexing pipeline')}</span>
              <span>{t('storage layer')}</span>
            </div>
            <div className="broad-skeleton">
              <div className="skel-file"><code>cmd/server/main.go</code><pre>{'func main()\nfunc newServer(cfg Config) *Server'}</pre></div>
              <div className="skel-file"><code>internal/indexer/service.go</code><pre>{'func (s *Service) SyncRepository(...)\nfunc (s *Service) Search(...)'}</pre></div>
              <div className="skel-file"><code>internal/store/store.go</code><pre>{'type Store interface { ... }\nfunc NewPostgres(dsn string) (*Postgres, error)'}</pre></div>
            </div>
          </div>
        </div>
      </section>

      <section {...screen('enhance-screen')}>
        <div className="landing-enhance">
          <div className="enhance-head">
            {eyebrow('enhance-screen','Prompt enhancement')}
            <h2>{t('From a vague question to a precise prompt')}</h2>
            <p className="section-lead">{t('The enhancer rewrites your question against the code it retrieved: real files and line ranges, a concrete approach, and a way to verify the result.')}</p>
          </div>
          <div className="enhance-panel" aria-hidden="true">
            <div className="enh-card">
              <span className="enh-label">{t('Your question')}</span>
              <p className="enh-question">{t('fix the login failure')}</p>
            </div>
            <div className="enh-arrow"><ArrowRight size={16}/></div>
            <div className="enh-card enh-after">
              <span className="enh-label">{t('Enhanced prompt')}</span>
              <div className="enh-output">
                <span className="enh-line">{t('Fix the login failure. Token validation lives in internal/auth/middleware.go (lines 12–48); credentials are checked and the session issued in internal/auth/service.go (lines 90–121).')}</span>{' '}
                <span className="enh-line">{t('First reproduce the failing request, then trace where the session cookie is set and compare it with what the middleware reads.')}</span>{' '}
                <span className="enh-line">{t('Verify by signing in through ui/src/pages/Login.tsx and confirming the next API call returns 200.')}<i className="enh-caret"/></span>
              </div>
            </div>
          </div>
          <div className="enh-tags">
            <span>{t('Grounded in retrieved code')}</span>
            <span>{t('Concrete approach')}</span>
            <span>{t('Verification step')}</span>
            <span>{t('Answers in your language')}</span>
          </div>
        </div>
      </section>

      <section {...screen('change-screen')}>
        <div className="landing-change">
          <div className="change-intro">
            {eyebrow('change-screen','Pay only for change')}
            <h2>{t('Index once, pay only for what changed')}</h2>
            <p className="section-lead">{t('Content addressing turns re-indexing into a diff, and every limit on your account is visible before you hit it.')}</p>
            <div className="diff-meter" aria-hidden="true">
              <div className="diff-head"><span>{t('Checkpoint after a commit')}</span><strong>{roll('change-screen','3 / 1,284','0 / 0')}</strong></div>
              <div className="diff-grid">{Array.from({length:96},(_,i)=><i key={i} className={i===17||i===52||i===80?'hot':''}/>)}</div>
              <small>{t('files uploaded / files in the project')}</small>
            </div>
          </div>
          <div className="change-list">
            <div className="change-row"><span className="change-icon"><UploadCloud size={18}/></span><div><h3>{t('Diff-only upload')}</h3><p>{t('Before uploading, the client asks which blobs are missing; you are only charged for content the server has never seen.')}</p></div></div>
            <div className="change-row"><span className="change-icon"><GitBranch size={18}/></span><div><h3>{t('Shared across branches')}</h3><p>{t('Branches and worktrees share identical content, so storage counts each file version once.')}</p></div></div>
            <div className="change-row"><span className="change-icon"><Shield size={18}/></span><div><h3>{t('Quotas you can see')}</h3><p>{t('Requests per minute, daily calls and storage are shown on your account page — no surprise bills.')}</p></div></div>
            <div className="change-row"><span className="change-icon"><Trash2 size={18}/></span><div><h3>{t('Idle projects clean themselves up')}</h3><p>{t('Projects untouched for 60 days are purged automatically; change the window or turn it off any time.')}</p></div></div>
            <div className="change-row"><span className="change-icon"><Clock size={18}/></span><div><h3>{t('Repeat questions are instant')}</h3><p>{t('Identical queries on the same checkpoint are served from cache.')}</p></div></div>
          </div>
        </div>
      </section>

      <section {...screen('start-screen')}>
        <div className="landing-mcp">
          <div className="mcp-copy">
            {eyebrow('start-screen','Getting started')}
            <h2>{t('Connect your agent in three steps')}</h2>
            <p>{t('Use the MCP server, or install BCE as an Agent Skill when your tool has no MCP. The first search uploads and indexes the project.')}</p>
            <div className="steps-grid">
              <div className="step"><span className="step-index">01</span><h3>{t('Generate a token')}</h3><p>{t('Create your personal BCE token in account settings.')}</p></div>
              <div className="step"><span className="step-index">02</span><h3>{t('Connect your agent')}</h3><p>{t('Add the MCP configuration, or save the SKILL.md into your agent’s skills folder.')}</p></div>
              <div className="step"><span className="step-index">03</span><h3>{t('Search from your editor')}</h3><p>{t('The first search uploads and indexes the project automatically.')}</p></div>
            </div>
          </div>
          <div className="entry-col">
            <div className="tab-switch entry-tabs glide" role="tablist" aria-label={t('Connection method')} ref={entryTabs.ref}>
              {entryTabs.layer}
              <button type="button" role="tab" aria-selected={entry==='mcp'} className={entry==='mcp'?'active':''} onClick={()=>setEntry('mcp')}>MCP</button>
              <button type="button" role="tab" aria-selected={entry==='skill'} className={entry==='skill'?'active':''} onClick={()=>setEntry('skill')}>Agent Skill</button>
            </div>
            {entry==='mcp'
              ?<McpConfigPanel/>
              :<div className="mcp-panel"><div className="win-chrome"><i/><i/><i/><span>shell</span><Button auto scale={0.55} type="secondary" className="brand-btn" icon={<Copy/>} onClick={()=>copySkill(skillSnippet)}><TextSwap text={skillCopied?t('Copied'):t('Copy')}/></Button></div><pre><code>{skillSnippet}</code></pre></div>}
            <p className="install-note">{entry==='mcp'
              ?<>{t('Full client list and options on the')} <button type="button" className="inline-link" onClick={()=>{location.hash='mcp'}}>{t('bce-tool page')}</button>.</>
              :<>{t('SKILL.md download and one-message install on the')} <button type="button" className="inline-link" onClick={()=>{location.hash='skill'}}>{t('Agent Skill page')}</button>.</>}</p>
          </div>
        </div>
        <div className="tools-strip">
          <span className="tools-label">{t('Works with your agent tools')}</span>
          <div className="tools-list glide" ref={tools.ref} {...tools.handlers}>
            {tools.layer}
            <span className="tool"><CursorMark/>Cursor</span>
            <span className="tool"><ClaudeMark/>Claude Code</span>
            <span className="tool"><CodexMark/>Codex</span>
            <span className="tool"><VSCodeMark/>VS Code</span>
            <span className="tool"><ZedMark/>Zed</span>
          </div>
        </div>
      </section>

      <section {...screen('end-screen',' screen-end')}>
        <div className="landing-end">
          <BrandLogo size={36}/>
          <h2>{t('Less context. Sharper answers.')}</h2>
          <p className="end-lead">{t('Hosted in our cloud, ready in minutes — or privately deployed when code must stay in.')}</p>
          <Button auto type="secondary" className="brand-btn" onClick={onSignIn}>{t('Get started')}</Button>
        </div>
        <div className="end-index glide" ref={endCells.ref} {...endCells.handlers}>
          {endCells.layer}
          {chapters.map(c=><button key={c.id} type="button" className="end-cell" onClick={()=>scrollToScreen(c.id)}><span className="funnel-index">{chapterNo(c.id)}</span><strong>{t(c.title)}</strong><small>{t(c.sub)}</small><ArrowUpRight size={14}/></button>)}
        </div>
        <SiteFooter github/>
      </section>
    </main>
    <button type="button" className={`back-top ${showTop?'show':''}`} aria-label={t('Back to top')} onClick={scrollToTop}><ArrowUp size={16}/></button>
  </div>
}

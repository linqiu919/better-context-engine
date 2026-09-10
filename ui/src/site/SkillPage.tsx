import { useState } from 'react'
import { Button } from '@geist-ui/core'
import { ArrowDown, Download } from '@geist-ui/icons'
import { useI18n } from '../i18n'
import { prefersReducedMotion } from '../lib/motion'
import { useCopy } from '../hooks/useCopy'
import { LangThemeSwitch } from '../components/LangThemeSwitch'
import { PublicNav, SiteFooter } from './PublicChrome'
import skillMd from '../../../skills/bce/SKILL.md?raw'

// Public Skill page ('#skill'): the Agent Skills (SKILL.md) way of using BCE
// without an MCP server — the agent reads the skill and shells out to
// `bce-tool --search`. Sibling of McpPage: same glass nav, paper tokens and
// section rhythm. The SKILL.md shown here is the repo's skills/bce/SKILL.md
// imported verbatim, so the page can never drift from the file users install.
const envSnippet=`export BCE_BASE_URL="https://bce.wxnext.top"
export BCE_TOKEN="bce_..."`
const searchSnippet=`npx -y bce-tool --base-url "$BCE_BASE_URL" --token "$BCE_TOKEN" --search "where is login rate limiting handled"`
const skillPaths:[string,string,string][]=[
  ['Cursor','.cursor/skills/bce/SKILL.md','~/.cursor/skills/bce/SKILL.md'],
  ['Claude Code','.claude/skills/bce/SKILL.md','~/.claude/skills/bce/SKILL.md'],
  ['Codex','.agents/skills/bce/SKILL.md','~/.agents/skills/bce/SKILL.md'],
  ['GitHub Copilot','.github/skills/bce/SKILL.md','—'],
]
// agentSetup is the message a user pastes into their coding agent: it carries
// the SKILL.md inline (heredoc) so the agent needs no network access to this
// page, states the env-var contract, and ends with a verification run. Kept in
// English on purpose — it is addressed to the agent, not the reader.
const agentSetup=(path:string)=>`Set up the Better Context Engine (BCE) Agent Skill for this project.

1. Create the skill file. Run exactly this command from the project root (the heredoc body is the skill, do not edit it):

mkdir -p ${path.slice(0,path.lastIndexOf('/'))} && cat > ${path} <<'BCE_SKILL_EOF'
${skillMd.trimEnd()}
BCE_SKILL_EOF

2. Make sure BCE_BASE_URL and BCE_TOKEN are exported in my shell profile (~/.zshrc, ~/.bashrc or the PowerShell profile). BCE_BASE_URL is https://bce.wxnext.top. If BCE_TOKEN is not set, ask me for it — never guess or invent a token.

3. Verify the skill works:

cd <project-root> && npx -y bce-tool --base-url "$BCE_BASE_URL" --token "$BCE_TOKEN" --search "where is the application entry point"

Confirm the output contains <codebase_context>. The first run indexes the whole project and may take a while; later runs are incremental.

4. From now on, follow the skill: use bce-tool --search for natural-language, cross-file or "where is X handled" questions about this codebase before reading or editing code.`

// downloadSkill saves the bundled SKILL.md as a file: a Blob URL on a
// transient <a download> — no server route needed, the content is already in
// the bundle via the ?raw import.
const downloadSkill=()=>{
  const url=URL.createObjectURL(new Blob([skillMd],{type:'text/markdown;charset=utf-8'}))
  const a=document.createElement('a')
  a.href=url;a.download='SKILL.md';a.rel='noopener'
  document.body.appendChild(a);a.click();a.remove()
  URL.revokeObjectURL(url)
}

export function SkillPage({dark,toggleTheme}:{dark:boolean;toggleTheme:()=>void}){
  const {t}=useI18n()
  const [scrolled,setScrolled]=useState(false)
  const [copied,copy]=useCopy()
  const [setupCopied,copySetup]=useCopy()
  const [agent,setAgent]=useState(0)
  const goHome=()=>{location.hash=''}
  const goLogin=()=>{location.hash='login'}
  const goMcp=()=>{location.hash='mcp'}
  const scrollToInstall=()=>{document.getElementById('skill-install')?.scrollIntoView({behavior:prefersReducedMotion()?'auto':'smooth'})}
  const traits:[string,string][]=[
    ['No resident process','Nothing runs in the background — the agent shells out to bce-tool only when it decides to search.'],
    ['Portable','One SKILL.md works across Cursor, Claude Code, Codex, GitHub Copilot and every other agent that reads the open Agent Skills format.'],
    ['Usage guidance built in','The skill tells the agent when to search, how to phrase queries and how to read the result — richer than a tool description.'],
    ['Shares the MCP cache','Skill and MCP use the same .ace-tool/index.bin, so mixing both in one project costs nothing extra.'],
  ]
  return <div className="mcp-page" onScroll={e=>setScrolled(e.currentTarget.scrollTop>24)}>
    <PublicNav scrolled={scrolled} onBrand={goHome} brandLabel={t('Back to home')}>
      <LangThemeSwitch dark={dark} toggleTheme={toggleTheme}/>
      <Button auto scale={0.75} type="secondary" onClick={goLogin}>{t('Get started')}</Button>
    </PublicNav>
    <main className="mcp-main">
      <section className="mcp-hero">
        <span className="hero-eyebrow"><i/>{t('Agent Skill · Open standard')}</span>
        <h1>bce skill</h1>
        <p>{t('Use BCE without an MCP server: a SKILL.md your coding agent reads, then runs bce-tool from the shell.')}</p>
        <div className="landing-cta"><Button auto type="secondary" className="brand-btn" onClick={goLogin}>{t('Get started')}</Button><button type="button" className="ghost-link" onClick={scrollToInstall}>{t('View installation')}<ArrowDown size={13}/></button></div>
        <div className="spec-strip" aria-hidden="true">
          <span><em>FORMAT</em>SKILL.md</span>
          <span><em>RUNTIME</em>npx bce-tool</span>
          <span><em>TRANSPORT</em>shell · stdout</span>
          <span><em>AGENTS</em>Cursor · Claude Code · Codex · Copilot</span>
        </div>
      </section>
      <section className="mcp-section">
        <span className="section-eyebrow">{t('How it works')}</span>
        <h2>{t('From SKILL.md to search result')}</h2>
        <p className="section-lead">{t('A skill is a folder with a SKILL.md. The agent loads it when a task matches, follows the instructions, and calls bce-tool in one-shot mode — the result comes back on stdout.')}</p>
        <div className="flow-diagram" aria-hidden="true">
          <div className="flow-node"><strong>{t('AI coding tool')}</strong><small>Cursor · Claude Code · Codex · Copilot</small></div>
          <div className="flow-link"><span>SKILL.md</span><i/></div>
          <div className="flow-node flow-node-core"><strong>bce-tool --search</strong><small>{t('one-shot · indexes then retrieves')}</small></div>
          <div className="flow-link"><span>HTTPS</span><i/></div>
          <div className="flow-node"><strong>{t('BCE service')}</strong><small>{t('retrieval & enhancement')}</small></div>
        </div>
      </section>
      <section className="mcp-section">
        <span className="section-eyebrow">{t('Skill vs MCP')}</span>
        <h2>{t('Same engine, lighter footprint')}</h2>
        <div className="feat-list">
          {traits.map(([title,desc])=><div className="feat-item" key={title}><strong>{t(title)}</strong><p>{t(desc)}</p></div>)}
        </div>
        <p className="install-note">{t('Prefer a persistent tool your agent can call natively?')} <button type="button" className="inline-link" onClick={goMcp}>{t('Use the MCP server instead')}</button></p>
      </section>
      <section className="mcp-section" id="skill-install">
        <span className="section-eyebrow">{t('Install')}</span>
        <h2>{t('Three steps, no daemon')}</h2>
        <div className="steps-grid skill-steps">
          <div className="step"><span className="step-index">01</span><h3>{t('Generate a token')}</h3><p>{t('Create your personal BCE token in account settings.')}</p></div>
          <div className="step"><span className="step-index">02</span><h3>{t('Export two variables')}</h3><p>{t('Put BCE_BASE_URL and BCE_TOKEN in your shell profile; the skill reads them at run time.')}</p></div>
          <div className="step"><span className="step-index">03</span><h3>{t('Save the SKILL.md')}</h3><p>{t('Copy it into your agent’s skills directory. The agent picks it up on the next session.')}</p></div>
        </div>
        <div className="term-panel">
          <div className="win-chrome"><i/><i/><i/><span>~/.zshrc</span></div>
          <pre><code><span className="term-prompt">{envSnippet}</span></code></pre>
        </div>
        <div className="term-panel">
          <div className="win-chrome"><i/><i/><i/><span>terminal</span></div>
          <pre><code><span className="term-prompt">$ {searchSnippet}</span>{'\n'}<span className="term-ok">✓</span> <span className="term-out">project indexed (incremental)</span>{'\n'}<span className="term-out">&lt;codebase_context&gt; … &lt;/codebase_context&gt;</span></code></pre>
        </div>
        <div className="mcp-panel skill-panel">
          <div className="win-chrome"><i/><i/><i/><span>skills/bce/SKILL.md</span><Button auto scale={0.55} type="secondary" className="brand-btn" onClick={()=>copy(skillMd)}>{copied?t('Copied'):t('Copy SKILL.md')}</Button><Button auto scale={0.55} type="secondary" className="brand-btn" icon={<Download/>} onClick={downloadSkill}>{t('Download SKILL.md')}</Button></div>
          <pre><code>{skillMd}</code></pre>
        </div>
        <div className="param-table path-table">
          <div className="param-row path-row param-head"><span>{t('Agent')}</span><span>{t('Project skill')}</span><span>{t('Personal skill')}</span></div>
          {skillPaths.map(([agent,project,personal])=><div className="param-row path-row" key={agent}><span>{agent}</span><span><code>{project}</code></span><span>{personal==='—'?personal:<code>{personal}</code>}</span></div>)}
        </div>
        <p className="install-note">{t('Requires bce-tool 0.0.5 or newer (the --search flag). Windows PowerShell users set $env:BCE_BASE_URL and $env:BCE_TOKEN instead.')}</p>
      </section>
      <section className="mcp-section" id="skill-agent">
        <span className="section-eyebrow">{t('For your agent')}</span>
        <h2>{t('Or paste one message and let the agent install it')}</h2>
        <p className="section-lead">{t('Pick your tool, copy the message, paste it into the agent chat. It carries the full SKILL.md inline, so the agent writes the file itself, checks your environment variables and runs a first search to verify.')}</p>
        <div className="tab-switch skill-agent-tabs" role="tablist" aria-label={t('Agent')}>
          {skillPaths.map(([name],i)=><button key={name} type="button" role="tab" aria-selected={agent===i} className={agent===i?'active':''} onClick={()=>setAgent(i)}>{name}</button>)}
        </div>
        <div className="mcp-panel skill-panel">
          <div className="win-chrome"><i/><i/><i/><span>{t('message to your agent')} · {skillPaths[agent][1]}</span><Button auto scale={0.55} type="secondary" className="brand-btn" onClick={()=>copySetup(agentSetup(skillPaths[agent][1]))}>{setupCopied?t('Copied'):t('Copy message')}</Button></div>
          <pre><code>{agentSetup(skillPaths[agent][1])}</code></pre>
        </div>
        <p className="install-note">{t('The message asks the agent to request your token rather than guess it — keep BCE_TOKEN out of the chat if the agent logs conversations.')}</p>
      </section>
      <section className="mcp-section mcp-end">
        <h2>{t('Teach your agent to look before it edits.')}</h2>
        <Button auto type="secondary" className="brand-btn" onClick={goLogin}>{t('Get started')}</Button>
      </section>
      <SiteFooter/>
    </main>
  </div>
}

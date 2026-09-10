import { Button } from '@geist-ui/core'
import { useI18n } from '../i18n'
import { useCopy } from '../hooks/useCopy'
import { mcpConfig } from './mcpConfig'

// McpConfigPanel is the mcp.json window (chrome bar with copy button + code)
// shown on both the landing MCP screen and the bce-tool install section.
export function McpConfigPanel(){
  const {t}=useI18n()
  const [copied,copy]=useCopy()
  return <div className="mcp-panel">
    <div className="win-chrome"><i/><i/><i/><span>mcp.json</span><Button auto scale={0.55} type="secondary" className="brand-btn" onClick={()=>copy(mcpConfig)}>{copied?t('Copied'):t('Copy configuration')}</Button></div>
    <pre><code>{mcpConfig}</code></pre>
  </div>
}

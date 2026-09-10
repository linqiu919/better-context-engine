import { Activity, Archive, BarChart2, Bell, Command, Database, Search, Server, Settings, Sliders, TrendingUp, Users, Zap } from '@geist-ui/icons'
import type { User } from './types'

export type Page = 'overview'|'repositories'|'search'|'mcp-usage'|'account'|'users'|'analytics'|'all-repositories'|'mcp-admin'|'infrastructure'|'quota'|'announcements'|'audit'
export type AuthState = { user:User; csrf_token:string }
export type NavEntry = { page:Page; label:string; icon:typeof Archive }

// External documentation site, opened in a new tab from the landing and
// console top bars.
export const DOCS_URL='https://bce-doc.wxnext.top'

export const userNav:NavEntry[] = [
  {page:'overview',label:'Overview',icon:BarChart2},
  {page:'repositories',label:'My projects',icon:Archive},
  {page:'search',label:'Search inspect',icon:Search},
  {page:'mcp-usage',label:'MCP usage',icon:Activity},
  {page:'account',label:'Account settings',icon:Settings},
]
export const adminNav:NavEntry[] = [
  {page:'users',label:'User management',icon:Users},
  {page:'analytics',label:'Analytics',icon:TrendingUp},
  {page:'all-repositories',label:'All projects',icon:Database},
  {page:'mcp-admin',label:'MCP management',icon:Zap},
  {page:'infrastructure',label:'System settings',icon:Server},
  {page:'quota',label:'Quota & rate limits',icon:Sliders},
  {page:'announcements',label:'Announcements',icon:Bell},
  {page:'audit',label:'Audit log',icon:Command},
]
export const allNav:NavEntry[] = [...userNav,...adminNav]

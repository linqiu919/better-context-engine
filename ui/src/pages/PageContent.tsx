import type { AuthState, Page } from '../nav'
import { OverviewPage } from './OverviewPage'
import { RepositoriesPage } from './RepositoriesPage'
import { SearchPage } from './SearchPage'
import { McpUsagePage } from './McpUsagePage'
import { AccountPage } from './AccountPage'
import { UsersPage } from './UsersPage'
import { AnalyticsPage } from './AnalyticsPage'
import { McpAdminPage } from './McpAdminPage'
import { InfrastructurePage } from './InfrastructurePage'
import { QuotaPage } from './QuotaPage'
import { AnnouncementsPage } from './AnnouncementsPage'
import { AuditPage } from './AuditPage'

// PageContent maps the active console page to its screen; `refresh` bumps on
// SSE index events so data pages can reload.
export function PageContent({page,auth,refresh}:{page:Page;auth:AuthState;refresh:number}){
  switch(page){
    case'repositories':return <RepositoriesPage refresh={refresh}/>
    case'search':return <SearchPage/>
    case'mcp-usage':return <McpUsagePage/>
    case'account':return <AccountPage user={auth.user}/>
    case'users':return <UsersPage/>
    case'analytics':return <AnalyticsPage/>
    case'all-repositories':return <RepositoriesPage admin refresh={refresh}/>
    case'mcp-admin':return <McpAdminPage/>
    case'infrastructure':return <InfrastructurePage/>
    case'quota':return <QuotaPage/>
    case'announcements':return <AnnouncementsPage/>
    case'audit':return <AuditPage/>
    default:return <OverviewPage refresh={refresh}/>
  }
}

import { useEffect } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { ErrorBoundary } from '../components/ErrorBoundary'
import { Icon } from '../components/icons'
import { Tabs, useIsMobile } from '../components/ui'
import { GeneralTab } from './settings/General'
import { AgentsTab } from './settings/Agents'
import { NotificationsTab } from './settings/Notifications'
import { WorkspacesTab } from './settings/Workspaces'
import { ProfileTab } from './settings/Profile'
import { TokensTab } from './settings/Tokens'
import { PeopleTab } from './settings/People'
import { AboutTab } from './settings/About'

type TabId = 'general' | 'agents' | 'notifications' | 'workspaces' | 'profile' | 'tokens' | 'users' | 'about'

const TABS: { id: TabId; label: string; icon: string; sub: string; admin?: boolean }[] = [
  { id: 'general', label: 'General', icon: 'settings', sub: 'How the app looks and how the composer behaves.' },
  { id: 'agents', label: 'Agents', icon: 'bot', sub: 'Which agent, model, effort and permissions new threads start with — and what the server has installed.' },
  { id: 'notifications', label: 'Notifications', icon: 'bell', sub: 'A push when an agent finishes, gets stuck or needs your answer.' },
  { id: 'workspaces', label: 'Folders', icon: 'folder', sub: 'Folders on the server agents start in, one tap away.' },
  { id: 'profile', label: 'Profile', icon: 'user', sub: 'Your name, email and password.' },
  { id: 'tokens', label: 'Tokens', icon: 'key', sub: 'Personal tokens for the API.' },
  { id: 'users', label: 'Users', icon: 'users', sub: 'Everyone who has an account on this box, and who may run agents.', admin: true },
  { id: 'about', label: 'About', icon: 'info', sub: 'Version, the server and the Pocket Core link.' },
]

export default function Settings() {
  const { tab: raw } = useParams()
  const navigate = useNavigate()
  const { user } = useApp()
  const mobile = useIsMobile()
  const isAdmin = user?.role === 'admin'
  const tabs = TABS.filter(t => !t.admin || isAdmin)
  const tab: TabId = (tabs.find(t => t.id === raw)?.id) || 'general'
  const current = tabs.find(t => t.id === tab)!
  const go = (id: TabId) => navigate(`/settings/${id}`, { replace: true })

  useEffect(() => { document.querySelector('.set-content')?.scrollTo({ top: 0 }) }, [tab])
  useEffect(() => { document.title = `Settings — ${current.label}` }, [current.label])

  return (
    <div className="set-page">
      <header className="set-header">
        <button type="button" className="btn ghost icon-only" aria-label="Back" title="Back" onClick={() => navigate('/')}><Icon name="chevron-left" size={18} /></button>
        <img src="/favicon.svg" alt="" width={24} height={24} className="set-logo" />
        <h1>Settings</h1>
      </header>
      {mobile && <div className="set-tabs-mobile"><Tabs tabs={tabs.map(t => ({ id: t.id, label: t.label, icon: t.icon }))} value={tab} onChange={go} /></div>}
      <div className="set-body">
        {!mobile && (
          <nav className="set-nav" aria-label="Settings sections">
            {tabs.map(t => (
              <button key={t.id} type="button" className={`set-nav-item ${t.id === tab ? 'active' : ''}`} aria-current={t.id === tab ? 'page' : undefined} onClick={() => go(t.id)}>
                <Icon name={t.icon} size={16} />{t.label}
              </button>
            ))}
          </nav>
        )}
        <main className="set-content">
          <div className="set-inner">
            <div className="set-title">
              <h2>{current.label}</h2>
              <p>{current.sub}</p>
            </div>
            <ErrorBoundary where={`settings-${tab}`} key={tab}>
              {tab === 'general' && <GeneralTab />}
              {tab === 'agents' && <AgentsTab />}
              {tab === 'notifications' && <NotificationsTab />}
              {tab === 'workspaces' && <WorkspacesTab />}
              {tab === 'profile' && <ProfileTab />}
              {tab === 'tokens' && <TokensTab />}
              {tab === 'users' && <PeopleTab />}
              {tab === 'about' && <AboutTab />}
            </ErrorBoundary>
          </div>
        </main>
      </div>
    </div>
  )
}

import { ReactNode } from 'react'
import type { PageName } from '../App'
import { logout } from '../api'

interface NavItem {
  id: PageName
  label: string
  icon: ReactNode
}

const Icon = ({ d, size = 18 }: { d: string; size?: number }) => (
  <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <path d={d} />
  </svg>
)

const IconNodes = () => <Icon d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5" />
const IconACL = () => (
  <svg width={18} height={18} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <rect x="3" y="11" width="18" height="11" rx="2" ry="2" /><path d="M7 11V7a5 5 0 0 1 10 0v4" />
  </svg>
)
const IconRelay = () => (
  <svg width={18} height={18} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <polyline points="16 3 21 3 21 8" /><line x1="4" y1="20" x2="21" y2="3" /><polyline points="21 16 21 21 16 21" /><line x1="15" y1="15" x2="21" y2="21" /><line x1="4" y1="4" x2="9" y2="9" />
  </svg>
)
const IconKey = () => (
  <svg width={18} height={18} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4" />
  </svg>
)
const IconCert = () => (
  <svg width={18} height={18} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <path d="M12 15l-2 5 2-1.5L14 20l-2-5z" /><circle cx="12" cy="9" r="6" /><path d="M9 9h.01M15 9h.01M10 13a2 2 0 0 0 4 0" />
  </svg>
)

const IconTopology = () => (
  <svg width={18} height={18} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3a14 14 0 0 1 0 18 14 14 0 0 1 0-18" />
  </svg>
)

const NAV_ITEMS: NavItem[] = [
  { id: 'nodes', label: '节点', icon: <IconNodes /> },
  { id: 'topology', label: '拓扑', icon: <IconTopology /> },
  { id: 'firewall', label: '防火墙', icon: <IconACL /> },
  { id: 'keys', label: '注册密钥', icon: <IconKey /> },
  { id: 'certs', label: '证书 / CA', icon: <IconCert /> },
]

interface Props {
  children: ReactNode
  currentPage: PageName
  onNavigate: (p: PageName) => void
  onLogout: () => void
}

export default function Layout({ children, currentPage, onNavigate, onLogout }: Props) {
  async function handleLogout() {
    await logout().catch(() => {})
    onLogout()
  }

  const user = localStorage.getItem('admin_user') ?? 'admin'

  return (
    <div className="layout">
      <aside className="sidebar">
        <div className="sidebar-logo">
          CoreLink
          <span>Admin Console</span>
        </div>
        <nav className="sidebar-nav">
          {NAV_ITEMS.map(item => (
            <a
              key={item.id}
              href={`#${item.id}`}
              className={currentPage === item.id ? 'active' : ''}
              onClick={e => { e.preventDefault(); onNavigate(item.id) }}
            >
              {item.icon}
              {item.label}
            </a>
          ))}
        </nav>
        <div className="sidebar-footer">
          <div className="sidebar-user">用户: {user}</div>
          <button className="btn-secondary" style={{ width: '100%' }} onClick={handleLogout}>
            退出登录
          </button>
        </div>
      </aside>
      <main className="main-content">
        {children}
      </main>
    </div>
  )
}

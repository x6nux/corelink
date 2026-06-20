import { useState, useEffect } from 'react'
import { isLoggedIn } from './api'
import LoginPage from './pages/LoginPage'
import Layout from './components/Layout'
import NodesPage from './pages/NodesPage'
import TopologyPage from './pages/TopologyPage'
import ACLPage from './pages/ACLPage'
import KeysPage from './pages/KeysPage'
import CertsPage from './pages/CertsPage'

export type PageName = 'nodes' | 'topology' | 'firewall' | 'keys' | 'certs'

function getInitialPage(): PageName {
  const hash = window.location.hash.replace('#', '')
  if (hash === 'acl' || hash === 'relays') return 'nodes'
  const valid: PageName[] = ['nodes', 'topology', 'firewall', 'keys', 'certs']
  return (valid as string[]).includes(hash) ? (hash as PageName) : 'nodes'
}

export default function App() {
  const [loggedIn, setLoggedIn] = useState(isLoggedIn())
  const [page, setPage] = useState<PageName>(getInitialPage())

  useEffect(() => {
    const handler = () => setLoggedIn(isLoggedIn())
    window.addEventListener('storage', handler)
    return () => window.removeEventListener('storage', handler)
  }, [])

  function handleLogin() {
    setLoggedIn(true)
  }

  function handleLogout() {
    setLoggedIn(false)
    setPage('nodes')
  }

  function navigateTo(p: PageName) {
    setPage(p)
    window.location.hash = p
  }

  if (!loggedIn) {
    return <LoginPage onLogin={handleLogin} />
  }

  return (
    <Layout currentPage={page} onNavigate={navigateTo} onLogout={handleLogout}>
      {page === 'nodes' && <NodesPage />}
      {page === 'topology' && <TopologyPage />}
      {page === 'firewall' && <ACLPage />}
      {page === 'keys' && <KeysPage />}
      {page === 'certs' && <CertsPage />}
    </Layout>
  )
}

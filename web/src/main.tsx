import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import './styles/base.css'
import './styles/app.css'
import './styles/ai.css'
import './styles/settings.css'
import './styles/motion.css'
import './styles/pages.css'
import { AppProvider, useApp } from './lib/app-context'
import { Loading, ToastProvider } from './components/ui'
import { ErrorBoundary } from './components/ErrorBoundary'
import { trackViewport } from './lib/viewport'

trackViewport()
import Login from './pages/Login'
import Shell from './pages/Shell'
import Settings from './pages/Settings'

function Gate({ children }: { children: React.ReactNode }) {
  const { status, user, settings } = useApp()
  const loc = useLocation()
  if (!status) return <div className="boot"><img src="/favicon.svg" alt="" /><Loading /></div>
  if (status.needs_setup) return <Navigate to="/setup" replace />
  if (!user) return <Navigate to="/login" replace state={{ from: loc.pathname + loc.search + loc.hash }} />
  if (!settings) return <div className="boot"><img src="/favicon.svg" alt="" /><Loading /></div>
  return <>{children}</>
}

function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login mode="login" />} />
      <Route path="/setup" element={<Login mode="setup" />} />
      <Route path="/settings/:tab?" element={<Gate><ErrorBoundary where="settings"><Settings /></ErrorBoundary></Gate>} />
      <Route path="*" element={<Gate><ErrorBoundary where="app"><Shell /></ErrorBoundary></Gate>} />
    </Routes>
  )
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <BrowserRouter>
      <ErrorBoundary where="root"><ToastProvider><AppProvider><App /></AppProvider></ToastProvider></ErrorBoundary>
    </BrowserRouter>
  </React.StrictMode>,
)

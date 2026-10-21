import React from 'react'
import { Icon } from './icons'

export function PageHead({ title, sub, onMenu, actions, icon }: { title: string; sub?: React.ReactNode; onMenu?: () => void; actions?: React.ReactNode; icon?: string }) {
  return (
    <header className="page-head">
      {onMenu && <button type="button" className="btn ghost icon-only list-menu" aria-label="Menu" onClick={onMenu}><Icon name="menu" size={19} /></button>}
      {icon && <span className="page-ico"><Icon name={icon} size={17} /></span>}
      <div className="grow">
        <h1>{title}</h1>
        {sub && <p className="page-sub">{sub}</p>}
      </div>
      {actions && <div className="row page-actions">{actions}</div>}
    </header>
  )
}

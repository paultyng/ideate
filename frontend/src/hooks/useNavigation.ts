import { useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { GetLaunchConfig } from '../wailsjs/go/app/App'

interface NavigateEvent {
  view: string
  params?: Record<string, string>
}

function buildNavigationPath(view: string, params: Record<string, string>): string {
  // Views with path params (not query params)
  if (view === 'idea' && params.slug) {
    return `/idea/${params.slug}`
  }
  // Default: query params
  const search = new URLSearchParams(params)
  return search.toString() ? `/${view}?${search}` : `/${view}`
}

export function useWailsNavigation() {
  const navigate = useNavigate()

  useEffect(() => {
    // Check initial launch config (app started with subcommand)
    GetLaunchConfig().then((config) => {
      if (config.view && config.view !== 'dashboard') {
        const params = new URLSearchParams(config.params || {})
        const path = params.toString() ? `/${config.view}?${params}` : `/${config.view}`
        navigate(path)
      }
    })

    // Listen for navigate events from IPC server (CLI subcommands)
    const cancel = EventsOn('navigate', (event: NavigateEvent) => {
      const path = buildNavigationPath(event.view, event.params || {})
      navigate(path)
    })

    return cancel
  }, [navigate])
}

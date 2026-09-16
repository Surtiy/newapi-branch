import { useEffect } from 'react'
import { toast } from 'sonner'
import { useTranslation } from 'react-i18next'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

type Envelope<T> = { success: boolean; data: T }
type Channel = { id: number }
type Preview = { catalog_version: string }

export function BranchCatalogChangeNotice() {
  const role = useAuthStore((state) => state.auth.user?.role)
  const { t } = useTranslation()

  useEffect(() => {
    // Administrators and root users can manage branch synchronization too.
    if (role === undefined || role < ROLE.ADMIN) return
    let cancelled = false

    const check = async () => {
      try {
        const channels = await api.get<Envelope<{ channels: Channel[] }>>(
          '/api/option/branch_catalog',
          { skipErrorHandler: true, skipBusinessError: true }
        )
        for (const channel of channels.data.data.channels) {
          const preview = await api.post<Envelope<Preview>>(
            '/api/option/branch_catalog/preview',
            { channel_id: channel.id },
            { skipErrorHandler: true, skipBusinessError: true }
          )
          if (cancelled) return
          const key = `branch-catalog-version:${channel.id}`
          const previous = window.localStorage.getItem(key)
          const current = preview.data.data.catalog_version
          if (previous && previous !== current) {
            toast.warning(t('Main site models or groups changed. Please sync.'))
          }
          window.localStorage.setItem(key, current)
        }
      } catch {
        // The sync page reports detailed errors; login-time checking stays quiet.
      }
    }

    void check()
    return () => {
      cancelled = true
    }
  }, [role, t])

  return null
}

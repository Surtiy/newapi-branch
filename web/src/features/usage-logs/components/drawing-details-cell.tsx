import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import type { MidjourneyLog } from '../types'
import { DrawingDetailsDialog } from './dialogs/drawing-details-dialog'

export function DrawingDetailsCell(props: {
  log: MidjourneyLog
  isAdmin: boolean
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)

  return (
    <>
      <Button
        variant='link'
        size='xs'
        className='px-0 text-xs'
        onClick={() => setOpen(true)}
      >
        {t('View details')}
      </Button>
      {open && (
        <DrawingDetailsDialog
          log={props.log}
          isAdmin={props.isAdmin}
          open={open}
          onOpenChange={setOpen}
        />
      )}
    </>
  )
}

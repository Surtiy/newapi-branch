/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { ViewIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import type { TaskLog } from '../types'
import { TaskDetailsDialog } from './dialogs/task-details-dialog'

export function TaskDetailsCell(props: {
  log: TaskLog
  isAdmin: boolean
  isRoot: boolean
}) {
  const { t } = useTranslation()
  const [dialogOpen, setDialogOpen] = useState(false)
  const [section, setSection] = useState<'overview' | 'parameters'>('overview')

  return (
    <>
      <div className='flex max-w-[220px] flex-col items-start gap-1'>
        <div className='flex flex-wrap items-center gap-1'>
          <Button
            type='button'
            variant='outline'
            size='xs'
            onClick={() => {
              setSection('overview')
              setDialogOpen(true)
            }}
          >
            <HugeiconsIcon
              icon={ViewIcon}
              className='size-3'
              strokeWidth={2}
              aria-hidden='true'
            />
            {t('View details')}
          </Button>
          <Button
            type='button'
            variant='ghost'
            size='xs'
            onClick={() => {
              setSection('parameters')
              setDialogOpen(true)
            }}
          >
            {t('Request Parameters')}
          </Button>
        </div>
        {props.log.fail_reason ? (
          <span className='max-w-full truncate text-xs text-red-600 dark:text-red-400'>
            {props.log.fail_reason}
          </span>
        ) : null}
      </div>
      <TaskDetailsDialog
        log={props.log}
        isAdmin={props.isAdmin}
        isRoot={props.isRoot}
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        initialSection={section}
      />
    </>
  )
}

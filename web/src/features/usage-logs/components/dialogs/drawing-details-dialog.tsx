import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { formatLogQuota } from '@/lib/format'

import type { MidjourneyLog } from '../../types'
import { DetailRow, DetailSection } from './log-detail-layout'

interface DrawingDetailsDialogProps {
  log: MidjourneyLog
  isAdmin: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function DrawingDetailsDialog(props: DrawingDetailsDialogProps) {
  const { t } = useTranslation()
  const log = props.log
  const requestParams =
    log.request_params == null
      ? undefined
      : JSON.stringify(log.request_params, null, 2)

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Drawing Details')}
      description={t('View the complete details for this task')}
      contentClassName='min-w-0 overflow-hidden sm:max-w-2xl'
      bodyClassName='space-y-3'
    >
      <DetailSection label={t('Basic Information')}>
        <DetailRow label={t('Task ID')} value={log.mj_id || '-'} mono />
        <DetailRow label={t('Model')} value={log.model_name || '-'} mono />
        <DetailRow label={t('Request ID')} value={log.request_id || '-'} mono />
        <DetailRow
          label={t('Request Path')}
          value={log.request_path || '-'}
          mono
        />
        <DetailRow
          label={t('Cost')}
          value={log.quota == null ? '-' : formatLogQuota(log.quota)}
          mono
        />
      </DetailSection>
      <DetailSection label={t('Prompt')}>
        <div className='flex min-w-0 items-start gap-2'>
          <p className='min-w-0 flex-1 text-xs [overflow-wrap:anywhere] break-words whitespace-pre-wrap'>
            {log.prompt || '-'}
          </p>
          {log.prompt && (
            <CopyButton value={log.prompt} aria-label={t('Copy prompt')} />
          )}
        </div>
      </DetailSection>
      {log.prompt_en && (
        <DetailSection label={t('Prompt (EN)')}>
          <p className='text-xs [overflow-wrap:anywhere] break-words whitespace-pre-wrap'>
            {log.prompt_en}
          </p>
        </DetailSection>
      )}
      <DetailSection label={t('Request Parameters')}>
        {requestParams !== undefined ? (
          <>
            <CopyButton
              value={requestParams}
              size='sm'
              variant='outline'
              aria-label={t('Copy request parameters')}
            >
              {t('Copy')}
            </CopyButton>
            <p className='text-muted-foreground text-xs'>
              {t(
                'Saved request snapshot. Credentials and file contents are omitted; large values may be shortened.'
              )}
            </p>
            <pre className='max-h-[40dvh] min-w-0 overflow-auto p-2 font-mono text-xs break-all whitespace-pre-wrap'>
              {requestParams}
            </pre>
          </>
        ) : (
          <p className='text-muted-foreground text-xs'>
            {t('No saved request parameters for this task.')}
          </p>
        )}
      </DetailSection>
      {props.isAdmin && (
        <DetailSection label={t('Admin Only')}>
          <DetailRow label={t('Channel')} value={String(log.channel_id)} mono />
          <DetailRow label={t('Submit Result')} value={String(log.code)} mono />
        </DetailSection>
      )}
    </Dialog>
  )
}

import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import type { MidjourneyLog } from '../types'
import { ImageDialog } from './dialogs/image-dialog'

export function DrawingImagesCell(props: { log: MidjourneyLog }) {
  const { t } = useTranslation()
  const [selectedImage, setSelectedImage] = useState<string>()
  const images = [
    ...new Set(props.log.image_urls?.filter((url) => url.trim()) ?? []),
  ]
  if (images.length === 0 && props.log.image_url) {
    images.push(props.log.image_url)
  }

  if (images.length === 0) {
    return <span className='text-muted-foreground/60 text-xs'>-</span>
  }

  return (
    <>
      <div className='flex max-w-[220px] flex-col gap-1'>
        <span className='text-muted-foreground text-xs'>
          {t('Image count: {{count}}', { count: images.length })}
        </span>
        <div className='flex flex-wrap gap-1'>
          {images.map((url, index) => (
            <Button
              key={url}
              variant='link'
              size='xs'
              className='px-0 text-xs'
              onClick={() => setSelectedImage(url)}
              title={t('Click to view image')}
            >
              {images.length === 1
                ? t('View')
                : t('Image {{index}}', { index: index + 1 })}
            </Button>
          ))}
        </div>
      </div>
      {selectedImage && (
        <ImageDialog
          key={selectedImage}
          imageUrl={selectedImage}
          taskId={props.log.mj_id}
          open
          onOpenChange={(open) => {
            if (!open) setSelectedImage(undefined)
          }}
        />
      )}
    </>
  )
}

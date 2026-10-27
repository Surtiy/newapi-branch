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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Skeleton } from '@/components/ui/skeleton'
import { api } from '@/lib/api'

interface ImageDialogProps {
  imageUrl: string
  taskId?: string
  open: boolean
  onOpenChange: (open: boolean) => void
}

function getAuthenticatedImagePath(imageUrl: string): string | undefined {
  try {
    const url = new URL(imageUrl, window.location.href)
    if (
      url.origin === window.location.origin &&
      /^\/api\/mj\/\d+\/image\/\d+$/.test(url.pathname)
    ) {
      // Pass only a same-origin relative path to the credentialed API client.
      return `${url.pathname}${url.search}`
    }
  } catch {
    // Historical MJ URLs keep the existing direct-image behavior.
  }
  return undefined
}

function ImagePreview(props: { imageUrl: string }) {
  const { t } = useTranslation()
  const authenticatedPath = getAuthenticatedImagePath(props.imageUrl)
  const [imageSrc, setImageSrc] = useState<string | undefined>(
    authenticatedPath ? undefined : props.imageUrl
  )
  const [isLoading, setIsLoading] = useState(true)
  const [hasError, setHasError] = useState(false)

  useEffect(() => {
    if (!authenticatedPath) return

    const controller = new AbortController()
    let objectUrl: string | undefined

    async function loadImage(path: string) {
      try {
        const response = await api.get<Blob>(path, {
          responseType: 'blob',
          signal: controller.signal,
          // Each preview owns its request and cancellation lifecycle.
          disableDuplicate: true,
          skipErrorHandler: true,
        })
        if (controller.signal.aborted) return
        objectUrl = URL.createObjectURL(response.data)
        setImageSrc(objectUrl)
      } catch {
        if (controller.signal.aborted) return
        setIsLoading(false)
        setHasError(true)
      }
    }

    void loadImage(authenticatedPath)
    return () => {
      controller.abort()
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [authenticatedPath])

  const handleImageLoad = () => {
    setIsLoading(false)
    setHasError(false)
  }

  const handleImageError = () => {
    setIsLoading(false)
    setHasError(true)
  }

  return (
    <div className='py-4'>
      <div className='bg-muted/50 relative flex min-h-[300px] items-center justify-center rounded-lg border'>
        {/* Skeleton - show when loading or error */}
        {(isLoading || hasError) && (
          <Skeleton className='absolute inset-0 h-full w-full rounded-lg' />
        )}

        {/* Actual Image */}
        {imageSrc && (
          <img
            src={imageSrc}
            alt={t('Generated image')}
            className={`max-h-[550px] w-full rounded-lg object-contain ${
              isLoading || hasError ? 'opacity-0' : 'opacity-100'
            }`}
            onLoad={handleImageLoad}
            onError={handleImageError}
            loading='lazy'
          />
        )}

        {/* Error text overlay (shown on skeleton) */}
        {hasError && (
          <div className='absolute inset-0 flex items-center justify-center'>
            <p role='alert' className='text-muted-foreground text-sm'>
              {t('Failed to load image')}
            </p>
          </div>
        )}
      </div>

      {/* Image URL */}
      <div className='bg-muted mt-4 rounded-md p-3'>
        <p className='text-muted-foreground font-mono text-xs break-all'>
          {props.imageUrl}
        </p>
      </div>
    </div>
  )
}

export function ImageDialog(props: ImageDialogProps) {
  const { t } = useTranslation()
  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Image Preview')}
      description={
        props.taskId
          ? `${t('Task ID:')} ${props.taskId}`
          : t('View the generated image')
      }
      contentClassName='sm:max-w-3xl'
      contentHeight='auto'
      bodyClassName='space-y-4'
    >
      <ScrollArea className='max-h-[600px]'>
        {props.open && (
          <ImagePreview key={props.imageUrl} imageUrl={props.imageUrl} />
        )}
      </ScrollArea>
    </Dialog>
  )
}

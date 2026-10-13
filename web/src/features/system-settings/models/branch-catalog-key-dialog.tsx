import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { isAxiosError } from 'axios'
import { Loader2, Save } from 'lucide-react'
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { Dialog } from '@/components/dialog'
import { PasswordInput } from '@/components/password-input'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { api } from '@/lib/api'

export type BranchKeyResult = {
  channel_id: number
  groups?: string[]
  conflicts?: Array<{ model_name: string; groups: string[] }>
}

export function BranchCatalogKeyDialog(props: {
  channelId: number | null
  groups: string[]
  onClose: () => void
  onSaved: (data: BranchKeyResult) => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const form = useForm<{ apiKey: string }>({
    resolver: zodResolver(
      z.object({
        apiKey: z
          .string()
          .trim()
          .min(1, t('Enter your main site API key'))
          .max(4096),
      })
    ),
    defaultValues: { apiKey: '' },
  })
  useEffect(() => {
    form.reset({ apiKey: '' })
  }, [props.channelId, form])
  const save = useMutation({
    mutationFn: async (values: { apiKey: string }) => {
      const response = await api.post<{
        success: boolean
        message?: string
        data: BranchKeyResult
      }>(
        '/api/option/branch_catalog/config',
        {
          channel_id: props.channelId || 0,
          api_key: values.apiKey,
        },
        { skipErrorHandler: true, skipBusinessError: true }
      )
      if (!response.data.success) {
        throw new Error(response.data.message || t('Request failed'))
      }
      return response.data.data
    },
    onSuccess: async (data) => {
      form.reset({ apiKey: '' })
      await client.invalidateQueries({ queryKey: ['branch-catalog-channels'] })
      await client.invalidateQueries({
        queryKey: ['branch-catalog-preview'],
      })
      props.onSaved(data)
      props.onClose()
      toast.success(t('Main site API key saved'))
    },
    onError: (error) => {
      let message = t('Request failed')
      if (isAxiosError(error)) {
        message = error.response?.data?.message || error.message
      } else if (error instanceof Error) message = error.message
      form.setError('apiKey', { type: 'server', message })
    },
  })
  const close = () => {
    if (save.isPending) return
    form.reset({ apiKey: '' })
    props.onClose()
  }
  return (
    <Dialog
      open={props.channelId !== null}
      onOpenChange={(open) => {
        if (!open) close()
      }}
      title={props.channelId ? t('Replace API key') : t('Add API key')}
      contentClassName='sm:max-w-lg'
      showCloseButton={!save.isPending}
      footer={
        <>
          <Button variant='outline' disabled={save.isPending} onClick={close}>
            {t('Cancel')}
          </Button>
          <Button
            type='submit'
            form='branch-key-form'
            disabled={save.isPending}
          >
            {save.isPending ? (
              <Loader2 className='size-4 animate-spin' />
            ) : (
              <Save className='size-4' />
            )}
            {t('Save API key')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id='branch-key-form'
          className='space-y-4'
          onSubmit={form.handleSubmit((values) => save.mutate(values))}
        >
          {props.groups.length > 0 && (
            <p className='text-muted-foreground text-sm break-words'>
              {props.groups.join(' / ')}
            </p>
          )}
          <FormField
            control={form.control}
            name='apiKey'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Main site API key')}</FormLabel>
                <FormControl>
                  <PasswordInput
                    {...field}
                    className='[&_input]:pr-10'
                    autoComplete='new-password'
                    spellCheck={false}
                    maxLength={4096}
                    disabled={save.isPending}
                    placeholder={t('Enter your main site API key')}
                    onChange={(event) => {
                      field.onChange(event)
                      form.clearErrors('apiKey')
                    }}
                  />
                </FormControl>
                <FormMessage className='break-words whitespace-pre-line' />
              </FormItem>
            )}
          />
        </form>
      </Form>
    </Dialog>
  )
}

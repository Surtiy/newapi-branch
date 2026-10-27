import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AxiosError, AxiosHeaders, type AxiosAdapter } from 'axios'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { ImageDialog } from '../dialogs/image-dialog'

const path = '/api/mj/42/image/0'
const originalAdapter = api.defaults.adapter
const originalAuth = useAuthStore.getState().auth
const blob = new Blob(['image'], { type: 'image/png' })
const createObjectURL = vi.fn<(blob: Blob) => string>()
const revokeObjectURL = vi.fn<(url: string) => void>()
const animationsDescriptor = Object.getOwnPropertyDescriptor(
  Element.prototype,
  'getAnimations'
)

beforeEach(() => {
  Object.defineProperty(Element.prototype, 'getAnimations', {
    configurable: true,
    value: () => [],
  })
  createObjectURL.mockReset().mockReturnValue('blob:authenticated-image')
  revokeObjectURL.mockReset()
  vi.stubGlobal(
    'URL',
    class extends URL {
      static createObjectURL = createObjectURL
      static revokeObjectURL = revokeObjectURL
    }
  )
  useAuthStore.setState({
    auth: { ...originalAuth, accessToken: 'preview-token' },
  })
})

afterEach(() => {
  api.defaults.adapter = originalAdapter
  useAuthStore.setState({ auth: originalAuth })
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  if (animationsDescriptor) {
    Object.defineProperty(
      Element.prototype,
      'getAnimations',
      animationsDescriptor
    )
  } else {
    Reflect.deleteProperty(Element.prototype, 'getAnimations')
  }
})

test.each([path, `${window.location.origin}${path}`])(
  'same-site protected image %s uses the authenticated API client and a blob URL',
  async (imageUrl) => {
    const adapter = vi.fn<AxiosAdapter>(async (config) => ({
      data: blob,
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    }))
    api.defaults.adapter = adapter
    const view = render(
      <ImageDialog imageUrl={imageUrl} open onOpenChange={() => undefined} />
    )
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    const image = await screen.findByRole('img', { name: 'Generated image' })
    expect(image).toHaveAttribute('src', 'blob:authenticated-image')
    const config = adapter.mock.calls[0][0]
    expect(config.url).toBe(path)
    expect(config.responseType).toBe('blob')
    expect(config.headers.Authorization).toBe('Bearer preview-token')
    expect(createObjectURL).toHaveBeenCalledWith(blob)
    fireEvent.load(image)
    expect(image).toHaveClass('opacity-100')
    view.unmount()
    expect(config.signal?.aborted).toBe(true)
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:authenticated-image')
  }
)

test.each([
  'https://media.example/old-mj.png',
  'https://media.example/api/mj/42/image/0',
  '//media.example/api/mj/42/image/0',
  `${window.location.origin}.evil.example/api/mj/42/image/0`,
  '/legacy/mj.png',
])(
  'legacy or external image %s renders directly without credentialed API requests',
  async (imageUrl) => {
    const adapter = vi.fn<AxiosAdapter>()
    api.defaults.adapter = adapter
    render(
      <ImageDialog imageUrl={imageUrl} open onOpenChange={() => undefined} />
    )
    expect(
      await screen.findByRole('img', { name: 'Generated image' })
    ).toHaveAttribute('src', imageUrl)
    expect(adapter).not.toHaveBeenCalled()
    expect(createObjectURL).not.toHaveBeenCalled()
  }
)

test('a terminal 401 displays the preview error without exposing the protected path as an image source', async () => {
  // Model the terminal response after the client's existing auth refresh policy.
  const get = vi.spyOn(api, 'get').mockRejectedValue(
    new AxiosError('Unauthorized', 'ERR_BAD_REQUEST', undefined, undefined, {
      data: {},
      status: 401,
      statusText: 'Unauthorized',
      headers: {},
      config: { headers: new AxiosHeaders() },
    })
  )
  const user = userEvent.setup()
  render(<ImageDialog imageUrl={path} open onOpenChange={() => undefined} />)
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Failed to load image'
  )
  expect(screen.queryByRole('img')).not.toBeInTheDocument()
  expect(createObjectURL).not.toHaveBeenCalled()
  expect(get).toHaveBeenCalledWith(
    path,
    expect.objectContaining({ responseType: 'blob' })
  )
  await user.keyboard('{Escape}')
  get.mockRestore()
})

test('switching images aborts the previous request and revokes its URL, closing revokes the next URL', async () => {
  const adapter = vi.fn<AxiosAdapter>(async (config) => ({
    data: blob,
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  }))
  api.defaults.adapter = adapter
  createObjectURL
    .mockReturnValueOnce('blob:first')
    .mockReturnValueOnce('blob:second')
  const view = render(
    <ImageDialog imageUrl={path} open onOpenChange={() => undefined} />
  )
  expect(await screen.findByRole('img')).toHaveAttribute('src', 'blob:first')
  view.rerender(
    <ImageDialog
      imageUrl='/api/mj/42/image/1'
      open
      onOpenChange={() => undefined}
    />
  )
  expect(screen.queryByRole('img')).not.toBeInTheDocument()
  expect(await screen.findByRole('img')).toHaveAttribute('src', 'blob:second')
  expect(adapter.mock.calls[0][0].signal?.aborted).toBe(true)
  expect(revokeObjectURL).toHaveBeenCalledWith('blob:first')
  view.rerender(
    <ImageDialog
      imageUrl='/api/mj/42/image/1'
      open={false}
      onOpenChange={() => undefined}
    />
  )
  await waitFor(() =>
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:second')
  )
  expect(adapter.mock.calls[1][0].signal?.aborted).toBe(true)
})

test('unmounting an in-flight preview aborts it and a late response creates no object URL', async () => {
  let resolve: (value: { data: Blob }) => void = () => undefined
  const get = vi.spyOn(api, 'get').mockImplementation(
    () =>
      new Promise((done) => {
        resolve = done
      })
  )
  const view = render(
    <ImageDialog imageUrl={path} open onOpenChange={() => undefined} />
  )
  const config = get.mock.calls[0][1]
  view.unmount()
  expect(config?.signal?.aborted).toBe(true)
  await act(async () => {
    resolve({ data: blob })
  })
  expect(createObjectURL).not.toHaveBeenCalled()
  expect(revokeObjectURL).not.toHaveBeenCalled()
  get.mockRestore()
})

test('a closed protected preview performs no request', () => {
  const adapter = vi.fn<AxiosAdapter>()
  api.defaults.adapter = adapter
  render(
    <ImageDialog imageUrl={path} open={false} onOpenChange={() => undefined} />
  )
  expect(adapter).not.toHaveBeenCalled()
})

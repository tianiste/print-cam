import { expect, test } from '@playwright/test'

function json(body) {
  return {
    contentType: 'application/json',
    body: JSON.stringify(body)
  }
}

test('renders login and redirects unauthenticated camera list', async ({ page }) => {
  await page.goto('/login')
  await expect(page.getByRole('heading', { name: 'Secure camera access' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Continue' })).toBeVisible()

  await page.goto('/cameras')
  await expect(page).toHaveURL(/\/login$/)
})

test('manages cameras with mocked API responses', async ({ page }) => {
  let cameras = [{ id: 'cam-1', name: 'Printer camera', createdAt: new Date().toISOString() }]

  await page.route('**/api/auth/me', async (route) => {
    await route.fulfill(json({ id: 'user-1', email: 'admin@example.com' }))
  })
  await page.route('**/api/auth/csrf', async (route) => {
    await route.fulfill(json({ csrfToken: 'csrf-token' }))
  })
  await page.route('**/api/cameras/cam-1', async (route) => {
    const request = route.request()
    if (request.method() === 'POST') {
      const payload = JSON.parse(request.postData() || '{}')
      cameras = cameras.map((camera) => camera.id === 'cam-1' ? { ...camera, name: payload.name } : camera)
      await route.fulfill(json({ status: 'updated' }))
      return
    }
    if (request.method() === 'DELETE') {
      cameras = []
      await route.fulfill(json({ status: 'deleted' }))
      return
    }
    await route.fulfill({ status: 404, body: 'not found' })
  })
  await page.route('**/api/cameras', async (route) => {
    await route.fulfill(json(cameras))
  })

  await page.goto('/cameras')
  await expect(page.getByRole('heading', { name: 'Cameras' })).toBeVisible()
  await expect(page.getByLabel('Camera name')).toHaveValue('Printer camera')

  await page.getByLabel('Camera name').fill('Updated printer')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByText('Camera updated.')).toBeVisible()
  await expect(page.getByLabel('Camera name')).toHaveValue('Updated printer')

  page.on('dialog', (dialog) => dialog.accept())
  await page.getByRole('button', { name: 'Delete' }).click()
  await expect(page.getByText('Camera deleted.')).toBeVisible()
  await expect(page.getByText('No cameras yet.')).toBeVisible()
})

test('renders viewer route while authenticated', async ({ page }) => {
  await page.route('**/api/auth/me', async (route) => {
    await route.fulfill(json({ id: 'user-1', email: 'admin@example.com' }))
  })
  await page.route('**/api/auth/csrf', async (route) => {
    await route.fulfill(json({ csrfToken: 'csrf-token' }))
  })
  await page.route('**/api/cameras/cam-1/turn-credentials', async (route) => {
    await route.fulfill(json({ ttlSeconds: 3600, iceServers: [{ urls: ['stun:stun.l.google.com:19302'] }] }))
  })

  await page.goto('/cameras/cam-1/view')
  await expect(page.getByText('Viewer')).toBeVisible()
  await expect(page.getByRole('heading', { name: 'cam-1' })).toBeVisible()
})

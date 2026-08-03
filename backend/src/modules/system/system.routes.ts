import { Router } from 'express'
import { z } from 'zod'
import { prisma } from '../../config/prisma.js'
import { requireAuth, type AuthRequest } from '../../middleware/auth.middleware.js'
import { encryptText } from '../../utils/crypto.js'

export const systemRouter = Router()
systemRouter.use(requireAuth)

const scopes = [
  'https://www.googleapis.com/auth/drive',
  'https://www.googleapis.com/auth/userinfo.email',
  'https://www.googleapis.com/auth/userinfo.profile',
]

systemRouter.get('/google-config', async (req: AuthRequest, res, next) => {
  try {
    const config = await prisma.providerConfig.findFirst({ where: { userId: req.user!.id, provider: 'google_drive', status: 'active' }, orderBy: { createdAt: 'desc' } })
    const defaultRedirectUri = `${req.protocol}://${req.get('host')}/connected-accounts/google/callback`
    return res.json({ exists: Boolean(config), clientId: '', redirectUri: config?.redirectUri ?? defaultRedirectUri, hasSecret: Boolean(config?.clientSecretEncrypted), defaultRedirectUri })
  } catch (error) { return next(error) }
})

systemRouter.post('/google-config', async (req: AuthRequest, res, next) => {
  try {
    const body = z.object({ clientId: z.string().min(1), clientSecret: z.string().min(1), redirectUri: z.string().url().optional() }).parse(req.body)
    const redirectUri = body.redirectUri || `${req.protocol}://${req.get('host')}/connected-accounts/google/callback`
    await prisma.providerConfig.updateMany({ where: { userId: req.user!.id, provider: 'google_drive', status: 'active' }, data: { status: 'disabled' } })
    const config = await prisma.providerConfig.create({ data: { userId: req.user!.id, provider: 'google_drive', clientIdEncrypted: encryptText(body.clientId), clientSecretEncrypted: encryptText(body.clientSecret), redirectUri, scopes: JSON.stringify(scopes), status: 'active' } })
    return res.status(201).json({ message: 'Google OAuth credentials saved.', id: config.id })
  } catch (error) { return next(error) }
})

import { Router } from 'express'
import { google } from 'googleapis'
import { z } from 'zod'
import { env } from '../../config/env.js'
import { prisma } from '../../config/prisma.js'
import { requireAuth, type AuthRequest } from '../../middleware/auth.middleware.js'
import { decryptText, encryptText, hashToken, randomToken } from '../../utils/crypto.js'
import { hashPassword } from '../../utils/password.js'
import { createOAuthClient, syncGoogleQuota } from '../google/google.service.js'

export const connectedAccountRouter = Router()

function parseScopes(value: string): string[] {
  try {
    const parsed: unknown = JSON.parse(value)
    return Array.isArray(parsed) ? parsed.filter((scope): scope is string => typeof scope === 'string') : []
  } catch {
    return []
  }
}

connectedAccountRouter.get('/', requireAuth, async (req: AuthRequest, res, next) => {
  try {
    const accounts = await prisma.connectedAccount.findMany({
      where: { userId: req.user!.id, provider: 'google_drive', status: 'connected' },
      include: { storageAccount: true },
      orderBy: { createdAt: 'desc' },
    })
    return res.json({ accounts: accounts.map(({ accessTokenEncrypted: _a, refreshTokenEncrypted: _r, storageAccount, ...account }) => ({
      ...account,
      storageAccount: storageAccount ? {
        ...storageAccount,
        totalBytes: storageAccount.totalBytes?.toString() ?? null,
        usedBytes: storageAccount.usedBytes.toString(),
        availableBytes: storageAccount.availableBytes?.toString() ?? null,
        trashBytes: storageAccount.trashBytes?.toString() ?? null,
      } : null,
    })) })
  } catch (error) {
    return next(error)
  }
})

async function createGoogleConnectUrl(req: AuthRequest) {
  const query = z.object({ providerConfigId: z.string().min(1).optional() }).parse(req.query)
  const config = query.providerConfigId
    ? await prisma.providerConfig.findFirstOrThrow({ where: { id: query.providerConfigId, OR: [{ userId: req.user!.id }, { userId: null }], provider: 'google_drive', status: 'active' } })
    : await prisma.providerConfig.findFirstOrThrow({ where: { userId: req.user!.id, provider: 'google_drive', status: 'active' }, orderBy: { createdAt: 'desc' } })
  const state = randomToken()
  await prisma.oauthState.create({ data: { userId: req.user!.id, providerConfigId: config.id, flow: 'connect', stateHash: hashToken(state), expiresAt: new Date(Date.now() + 10 * 60_000) } })
  return createOAuthClient(config).generateAuthUrl({ access_type: 'offline', prompt: 'consent', include_granted_scopes: true, scope: parseScopes(config.scopes), state })
}

connectedAccountRouter.get('/google/connect-url', requireAuth, async (req: AuthRequest, res, next) => {
  try { return res.json({ url: await createGoogleConnectUrl(req) }) } catch (error) { return next(error) }
})

connectedAccountRouter.get('/google/connect', requireAuth, async (req: AuthRequest, res, next) => {
  try { return res.redirect(await createGoogleConnectUrl(req)) } catch (error) { return next(error) }
})

connectedAccountRouter.get('/google/callback', async (req, res) => {
  try {
    const query = z.object({ code: z.string(), state: z.string() }).parse(req.query)
    const oauthState = await prisma.oauthState.findUniqueOrThrow({ where: { stateHash: hashToken(query.state) }, include: { providerConfig: true } })
    if (oauthState.flow !== 'connect' || !oauthState.userId || oauthState.usedAt || oauthState.expiresAt < new Date()) throw new Error('Google OAuth state expired.')
    const client = createOAuthClient(oauthState.providerConfig)
    const { tokens } = await client.getToken(query.code)
    if (!tokens.access_token) throw new Error('Google did not return an access token.')
    client.setCredentials(tokens)
    const profile = await google.oauth2({ version: 'v2', auth: client }).userinfo.get()
    const providerAccountId = profile.data.id
    const email = profile.data.email
    if (!providerAccountId || !email) throw new Error('Google profile missing id or email.')
    const existing = await prisma.connectedAccount.findUnique({ where: { userId_provider_providerAccountId: { userId: oauthState.userId, provider: 'google_drive', providerAccountId } } })
    const refreshTokenEncrypted = tokens.refresh_token ? encryptText(tokens.refresh_token) : existing?.refreshTokenEncrypted
    if (!refreshTokenEncrypted) throw new Error('Google did not return a refresh token. Remove this app from Google account permissions then reconnect.')
    const account = await prisma.connectedAccount.upsert({
      where: { userId_provider_providerAccountId: { userId: oauthState.userId, provider: 'google_drive', providerAccountId } },
      create: { userId: oauthState.userId, providerConfigId: oauthState.providerConfigId, provider: 'google_drive', providerAccountId, email, displayName: profile.data.name, avatarUrl: profile.data.picture, accessTokenEncrypted: encryptText(tokens.access_token), refreshTokenEncrypted, tokenExpiresAt: new Date(tokens.expiry_date ?? Date.now() + 3600_000), scopes: oauthState.providerConfig.scopes, status: 'connected' },
      update: { providerConfigId: oauthState.providerConfigId, email, displayName: profile.data.name, avatarUrl: profile.data.picture, accessTokenEncrypted: encryptText(tokens.access_token), refreshTokenEncrypted, tokenExpiresAt: new Date(tokens.expiry_date ?? Date.now() + 3600_000), scopes: oauthState.providerConfig.scopes, status: 'connected' },
    })
    await prisma.oauthState.update({ where: { id: oauthState.id }, data: { usedAt: new Date() } })
    await syncGoogleQuota(account.id).catch(() => undefined)
    return res.redirect(`${env.FRONTEND_URL}/google-connected?status=success`)
  } catch (error) {
    console.error('Google OAuth callback failed:', error)
    return res.redirect(`${env.FRONTEND_URL}/google-connected?status=error`)
  }
})

connectedAccountRouter.post('/:id/sync-quota', requireAuth, async (req: AuthRequest, res, next) => {
  try {
    const account = await prisma.connectedAccount.findFirstOrThrow({ where: { id: String(req.params.id), userId: req.user!.id, provider: 'google_drive' } })
    const quota = await syncGoogleQuota(account.id)
    return res.json({ quota: { ...quota, totalBytes: quota.totalBytes?.toString() ?? null, usedBytes: quota.usedBytes.toString(), availableBytes: quota.availableBytes?.toString() ?? null, trashBytes: quota.trashBytes?.toString() ?? null } })
  } catch (error) { return next(error) }
})

connectedAccountRouter.delete('/:id', requireAuth, async (req: AuthRequest, res, next) => {
  try {
    await prisma.connectedAccount.updateMany({ where: { id: String(req.params.id), userId: req.user!.id, provider: 'google_drive' }, data: { status: 'disconnected' } })
    return res.json({ status: 'ok' })
  } catch (error) { return next(error) }
})

void decryptText
void hashPassword

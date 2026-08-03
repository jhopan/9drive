import { Router } from 'express'
import { google } from 'googleapis'
import { z } from 'zod'
import { prisma } from '../../config/prisma.js'
import { requireAuth, type AuthRequest } from '../../middleware/auth.middleware.js'
import { getAuthedGoogleClient, syncGoogleAppFolderFiles, syncGoogleQuota } from '../google/google.service.js'
import { streamGoogleFile } from './stream-google-file.js'

export const fileRouter = Router()
fileRouter.use(requireAuth)

fileRouter.get('/', async (req: AuthRequest, res, next) => {
  try {
    const query = z.object({ folderId: z.string().optional(), q: z.string().trim().max(255).optional(), accountId: z.string().optional() }).parse(req.query)
    const files = await prisma.file.findMany({
      where: { userId: req.user!.id, status: 'active', ...(query.folderId ? { folderId: query.folderId } : {}), ...(query.q ? { name: { contains: query.q } } : {}), ...(query.accountId ? { connectedAccountId: query.accountId } : {}) },
      include: { connectedAccount: { select: { id: true, email: true, provider: true } }, folder: { select: { id: true, name: true } } },
      orderBy: { createdAt: 'desc' },
    })
    return res.json({ files: files.map((file) => ({ ...file, sizeBytes: file.sizeBytes.toString() })) })
  } catch (error) { return next(error) }
})

fileRouter.post('/sync-google', async (req: AuthRequest, res, next) => {
  try {
    const { connectedAccountId } = z.object({ connectedAccountId: z.string().min(1).optional() }).parse(req.body ?? {})
    const accounts = await prisma.connectedAccount.findMany({ where: { userId: req.user!.id, provider: 'google_drive', status: 'connected', ...(connectedAccountId ? { id: connectedAccountId } : {}) }, select: { id: true } })
    const results = []
    for (const account of accounts) results.push(await syncGoogleAppFolderFiles(account.id, req.user!.id))
    return res.json({ status: 'ok', results })
  } catch (error) { return next(error) }
})

fileRouter.get('/:id/download', async (req: AuthRequest, res, next) => {
  try {
    const file = await prisma.file.findFirstOrThrow({ where: { id: String(req.params.id), userId: req.user!.id, status: 'active' }, include: { connectedAccount: true } })
    return streamGoogleFile(file, req.headers.range, res, { disposition: 'attachment' })
  } catch (error) { return next(error) }
})

fileRouter.patch('/:id', async (req: AuthRequest, res, next) => {
  try {
    const body = z.object({ name: z.string().min(1).max(255).optional(), folderId: z.string().nullable().optional() }).parse(req.body)
    const file = await prisma.file.findFirstOrThrow({ where: { id: String(req.params.id), userId: req.user!.id, status: 'active' }, include: { connectedAccount: true } })
    if (body.folderId) await prisma.folder.findFirstOrThrow({ where: { id: body.folderId, userId: req.user!.id, deletedAt: null } })
    if (body.name) await google.drive({ version: 'v3', auth: await getAuthedGoogleClient(file.connectedAccount) }).files.update({ fileId: file.providerFileId, requestBody: { name: body.name } })
    const updated = await prisma.file.update({ where: { id: file.id }, data: { ...(body.name ? { name: body.name } : {}), ...(body.folderId !== undefined ? { folderId: body.folderId } : {}) } })
    return res.json({ file: { ...updated, sizeBytes: updated.sizeBytes.toString() } })
  } catch (error) { return next(error) }
})

fileRouter.delete('/:id', async (req: AuthRequest, res, next) => {
  try {
    const file = await prisma.file.findFirstOrThrow({ where: { id: String(req.params.id), userId: req.user!.id, status: 'active' }, include: { connectedAccount: true } })
    await google.drive({ version: 'v3', auth: await getAuthedGoogleClient(file.connectedAccount) }).files.delete({ fileId: file.providerFileId })
    await prisma.file.delete({ where: { id: file.id } })
    await syncGoogleQuota(file.connectedAccountId).catch(() => undefined)
    return res.json({ status: 'ok' })
  } catch (error) { return next(error) }
})

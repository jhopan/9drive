import { useState, useEffect } from 'react'
import { Cloud, Trash2, Plus, Power } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { DummyModal } from '@/components/drive/DummyModal'
import { apiFetch } from '@/lib/api'

type OAuthConfig = {
  id: string
  label: string
  redirectUri: string
  status: 'active' | 'disabled'
  lastUsedAt: string
  createdAt: string
  quotaUsed: number
  quotaLimit: number
}

export function OAuthConfigManager() {
  const [configs, setConfigs] = useState<OAuthConfig[]>([])
  const [defaultRedirectUri, setDefaultRedirectUri] = useState('')
  const [loading, setLoading] = useState(true)
  const [addModalOpen, setAddModalOpen] = useState(false)
  const [newConfig, setNewConfig] = useState({ clientId: '', clientSecret: '', redirectUri: '', label: '' })
  const [adding, setAdding] = useState(false)
  const [message, setMessage] = useState('')

  async function load() {
    try {
      const data = await apiFetch<{ configs: OAuthConfig[]; defaultRedirectUri: string }>('/system/google-config')
      setConfigs(data.configs)
      setDefaultRedirectUri(data.defaultRedirectUri)
    } catch (error) {
      console.error('Failed to load configs:', error)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
    const interval = setInterval(load, 10000) // Auto-refresh every 10s
    return () => clearInterval(interval)
  }, [])

  async function addConfig() {
    setAdding(true)
    setMessage('')
    try {
      await apiFetch('/system/google-config', {
        method: 'POST',
        body: JSON.stringify({
          clientId: newConfig.clientId,
          clientSecret: newConfig.clientSecret,
          redirectUri: newConfig.redirectUri || defaultRedirectUri,
          label: newConfig.label,
        }),
      })
      setMessage('Config added successfully')
      setNewConfig({ clientId: '', clientSecret: '', redirectUri: '', label: '' })
      setAddModalOpen(false)
      load()
    } catch (error) {
      alert(error instanceof Error ? error.message : 'Failed to add config')
    } finally {
      setAdding(false)
    }
  }

  async function toggleStatus(id: string, currentStatus: string) {
    const newStatus = currentStatus === 'active' ? 'disabled' : 'active'
    try {
      await apiFetch(`/system/google-config/${id}`, {
        method: 'PATCH',
        body: JSON.stringify({ status: newStatus }),
      })
      load()
    } catch (error) {
      alert(error instanceof Error ? error.message : 'Failed to update status')
    }
  }

  async function deleteConfig(id: string, label: string) {
    if (!confirm(`Delete config "${label}"?`)) return
    try {
      await apiFetch(`/system/google-config/${id}`, { method: 'DELETE' })
      load()
    } catch (error) {
      alert(error instanceof Error ? error.message : 'Failed to delete config')
    }
  }

  if (loading) {
    return <Card className="p-4"><p className="text-sm text-slate-500">Loading OAuth configs...</p></Card>
  }

  return (
    <>
      <Card className="p-4">
        <div className="flex items-center justify-between border-b border-slate-100 pb-3 mb-4">
          <div className="flex items-center gap-2.5">
            <Cloud className="h-5 w-5 text-blue-600" />
            <h2 className="text-[17px] font-bold">Google OAuth Configs</h2>
          </div>
          <Button size="sm" onClick={() => setAddModalOpen(true)}>
            <Plus className="h-4 w-4" />Add Config
          </Button>
        </div>

        {message && <p className="mb-3 text-sm text-green-600">{message}</p>}

        <div className="grid gap-3">
          {configs.map((cfg) => {
            const quotaPercent = (cfg.quotaUsed / cfg.quotaLimit) * 100
            const isNearLimit = quotaPercent >= 80
            return (
              <div key={cfg.id} className="border border-slate-200 rounded-xl p-3">
                <div className="flex items-center justify-between mb-2">
                  <div className="flex items-center gap-2">
                    <span className="font-semibold text-sm">{cfg.label}</span>
                    <span className={`text-xs px-2 py-0.5 rounded ${cfg.status === 'active' ? 'bg-green-100 text-green-700' : 'bg-slate-100 text-slate-600'}`}>
                      {cfg.status}
                    </span>
                  </div>
                  <div className="flex items-center gap-1">
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 w-7 p-0"
                      onClick={() => toggleStatus(cfg.id, cfg.status)}
                      title={cfg.status === 'active' ? 'Disable' : 'Enable'}
                    >
                      <Power className="h-4 w-4" />
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 w-7 p-0 text-red-600 hover:text-red-700"
                      onClick={() => deleteConfig(cfg.id, cfg.label)}
                      title="Delete"
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                </div>

                <div className="text-xs text-slate-500 mb-2">
                  Quota: {cfg.quotaUsed.toLocaleString()} / {cfg.quotaLimit.toLocaleString()} requests per 100s
                </div>

                <div className="relative h-2 bg-slate-100 rounded-full overflow-hidden">
                  <div
                    className={`absolute inset-y-0 left-0 rounded-full transition-all ${isNearLimit ? 'bg-orange-500' : 'bg-blue-500'}`}
                    style={{ width: `${Math.min(quotaPercent, 100)}%` }}
                  />
                </div>
              </div>
            )
          })}
        </div>
      </Card>

      <DummyModal
        open={addModalOpen}
        onClose={() => setAddModalOpen(false)}
        title="Add OAuth Config"
        description="Add another Google Cloud project to distribute rate limits"
      >
        <div className="grid gap-4 py-2">
          <label className="grid gap-1.5">
            <span className="text-sm font-semibold">Label</span>
            <input
              className="h-10 rounded-xl border border-slate-200 px-3 text-sm"
              placeholder="Project 2"
              value={newConfig.label}
              onChange={(e) => setNewConfig({ ...newConfig, label: e.target.value })}
            />
          </label>

          <label className="grid gap-1.5">
            <span className="text-sm font-semibold">Client ID</span>
            <input
              className="h-10 rounded-xl border border-slate-200 px-3 text-sm"
              placeholder="xxx.apps.googleusercontent.com"
              value={newConfig.clientId}
              onChange={(e) => setNewConfig({ ...newConfig, clientId: e.target.value })}
              required
            />
          </label>

          <label className="grid gap-1.5">
            <span className="text-sm font-semibold">Client Secret</span>
            <input
              type="password"
              className="h-10 rounded-xl border border-slate-200 px-3 text-sm"
              placeholder="Enter secret"
              value={newConfig.clientSecret}
              onChange={(e) => setNewConfig({ ...newConfig, clientSecret: e.target.value })}
              required
            />
          </label>

          <label className="grid gap-1.5">
            <span className="text-sm font-semibold">Redirect URI (optional)</span>
            <input
              className="h-10 rounded-xl border border-slate-200 px-3 text-sm"
              placeholder={defaultRedirectUri}
              value={newConfig.redirectUri}
              onChange={(e) => setNewConfig({ ...newConfig, redirectUri: e.target.value })}
            />
          </label>

          <div className="flex justify-end gap-2 mt-2">
            <Button variant="outline" onClick={() => setAddModalOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={addConfig}
              disabled={adding || !newConfig.clientId || !newConfig.clientSecret}
            >
              {adding ? 'Adding...' : 'Add Config'}
            </Button>
          </div>
        </div>
      </DummyModal>
    </>
  )
}

import { useEffect } from 'react'
import { useSearchParams } from 'react-router-dom'

export default function GoogleConnectedPage() {
  const [params] = useSearchParams()
  const status = params.get('status')

  useEffect(() => {
    if (window.opener) {
      window.opener.postMessage(
        { type: 'GOOGLE_CONNECTED', status: status === 'success' ? 'success' : 'error' },
        window.location.origin
      )
      setTimeout(() => window.close(), 500)
    }
  }, [status])

  return (
    <div style={{ padding: '2rem', fontFamily: 'system-ui', textAlign: 'center' }}>
      <h1 style={{ fontSize: '1.5rem', marginBottom: '1rem' }}>
        {status === 'success' ? '✓ Connected' : '✗ Connection Failed'}
      </h1>
      <p style={{ color: '#666' }}>
        {status === 'success'
          ? 'Google Drive connected successfully. This window will close automatically.'
          : 'Connection failed. Please try again.'}
      </p>
    </div>
  )
}

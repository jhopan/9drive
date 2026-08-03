import { Navigate, Route, Routes } from 'react-router-dom'
import { ProtectedRoute } from '@/components/auth/ProtectedRoute'
import { DriveLayout } from '@/layouts/DriveLayout'
import { AllFilesPage } from '@/pages/AllFilesPage'
import { LoginPage } from '@/pages/LoginPage'
import { GoogleAuthPage } from '@/pages/GoogleAuthPage'
import { GoogleConnectedPage } from '@/pages/GoogleConnectedPage'
import { QuotaTrackerPage } from '@/pages/QuotaTrackerPage'
import { RegisterPage } from '@/pages/RegisterPage'
import { SettingsPage } from '@/pages/SettingsPage'
import { UploadProvider } from '@/context/UploadContext'

function App() {
  return (
    <UploadProvider>
      <Routes>
        <Route path="login" element={<LoginPage />} />
        <Route path="register" element={<RegisterPage />} />
        <Route path="google-auth" element={<GoogleAuthPage />} />
        <Route path="google-connected" element={<GoogleConnectedPage />} />
        <Route element={<ProtectedRoute />}>
          <Route element={<DriveLayout />}>
            <Route index element={<Navigate to="/all-files" replace />} />
            <Route path="all-files" element={<AllFilesPage />} />
            <Route path="quota" element={<QuotaTrackerPage />} />
            <Route path="settings" element={<SettingsPage />} />
          </Route>
        </Route>
        <Route path="*" element={<Navigate to="/all-files" replace />} />
      </Routes>
    </UploadProvider>
  )
}

export default App

/**
 * 面板登录认证
 * 使用独立密码登录
 */

const TOKEN_KEY = 'qwen2api_panel_token'

export function getPanelToken(): string {
  try {
    return localStorage.getItem(TOKEN_KEY) || ''
  } catch {
    return ''
  }
}

export function setPanelToken(token: string) {
  localStorage.setItem(TOKEN_KEY, token)
}

export function clearPanelToken() {
  localStorage.removeItem(TOKEN_KEY)
}

export function isLoggedIn(): boolean {
  return !!getPanelToken()
}

export function getAuthHeader(): Record<string, string> {
  const token = getPanelToken()
  if (!token) return {}
  return { Authorization: `Bearer ${token}` }
}

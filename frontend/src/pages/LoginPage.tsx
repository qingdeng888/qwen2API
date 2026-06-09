import { useState } from "react"
import { Button } from "../components/ui/button"
import { Lock } from "lucide-react"
import { API_BASE } from "../lib/api"
import { setPanelToken } from "../lib/auth"

interface Props {
  onLogin: () => void
}

export default function LoginPage({ onLogin }: Props) {
  const [password, setPassword] = useState("")
  const [error, setError] = useState("")
  const [loading, setLoading] = useState(false)

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!password.trim()) {
      setError("请输入面板密码")
      return
    }
    setLoading(true)
    setError("")

    try {
      const res = await fetch(`${API_BASE}/api/admin/login`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ password: password.trim() }),
      })
      const data = await res.json()
      if (res.ok && data.token) {
        setPanelToken(data.token)
        onLogin()
      } else {
        setError(data.error || "密码错误")
      }
    } catch {
      setError("连接服务器失败")
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-background p-4">
      <div className="w-full max-w-sm">
        <div className="rounded-2xl border border-border/50 bg-card/80 backdrop-blur-xl shadow-2xl p-8">
          <div className="text-center mb-8">
            <div className="inline-flex items-center justify-center w-16 h-16 rounded-full bg-primary/10 mb-4">
              <Lock className="h-8 w-8 text-primary" />
            </div>
            <h1 className="text-2xl font-extrabold tracking-tight bg-gradient-to-br from-indigo-500 to-purple-500 bg-clip-text text-transparent">
              qwen2API
            </h1>
            <p className="text-muted-foreground mt-2 text-sm">请输入面板密码登录管理控制台</p>
          </div>

          <form onSubmit={handleLogin} className="space-y-4">
            <div>
              <input
                type="password"
                value={password}
                onChange={e => { setPassword(e.target.value); setError("") }}
                placeholder="面板密码"
                autoFocus
                className="flex h-12 w-full rounded-lg border border-input bg-background px-4 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-primary/50 transition-all"
              />
            </div>

            {error && (
              <div className="rounded-lg bg-destructive/10 border border-destructive/20 px-3 py-2 text-sm text-destructive">
                {error}
              </div>
            )}

            <Button
              type="submit"
              disabled={loading}
              className="w-full h-12 text-base font-semibold"
            >
              {loading ? "验证中..." : "登 录"}
            </Button>
          </form>
        </div>
      </div>
    </div>
  )
}

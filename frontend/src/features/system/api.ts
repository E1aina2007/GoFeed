import { request } from '@/lib/api'

export function getHealth() {
  return request<{ name: string; status: string }>('/health')
}

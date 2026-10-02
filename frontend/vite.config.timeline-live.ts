import process from 'node:process'
import { mergeConfig } from 'vite'

import baseConfig from './vite.config'

const target = process.env.GOFEED_TIMELINE_API
if (!target || !/^http:\/\/127\.0\.0\.1:\d+$/.test(target)) {
  throw new Error('GOFEED_TIMELINE_API must point to the isolated loopback test API')
}

export default mergeConfig(baseConfig, {
  server: {
    host: '127.0.0.1',
    strictPort: true,
    proxy: {
      '/api': { target, changeOrigin: true },
      '/static': { target, changeOrigin: true },
    },
  },
})
